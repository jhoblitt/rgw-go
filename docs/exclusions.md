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
maintainable code base. The Ceph version floor is Squid 19.2.7 or Tentacle
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
  reply never carries an older stored encoding. The one exception is the
  `bi_*` methods, which return an entry's stored bytes. Of them rgw-go
  calls `bi_get` alone, and decodes what it reads with the decoders that
  take every version the class has written. It reads a name's OLH entry,
  as radosgw's `repair_olh` does
  ([rgw_rados.cc:8280](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8280),
  [rgw_rados.cc:9026](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9026)),
  and one version's entries, as radosgw's versioned delete reads one
  ([rgw_rados.cc:9443-9463](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9443-L9463),
  [rgw_rados.cc:10192-10212](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192-L10212)):
  one read for a named instance, and up to three for `null`, in the
  class's `read_key_entry` order, the plain key, then the null delete
  marker's key, then the null data key
  ([cls_rgw.cc:946-978](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978),
  [cls_rgw.cc:1076-1108](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108);
  "A versioned delete of versionId=null checks the null version itself",
  under Object write differences). A CompleteMultipartUpload or an
  AbortMultipartUpload that finds an earlier completion of a version by
  its record, or a CompleteMultipartUpload that finds one into its key's
  null version by that version's manifest, reads that version's entries the
  same way, and its list entry at the instance entry's versioned epoch, to
  learn whether its link landed, which radosgw's replay and abort of the
  record do not read ("A CompleteMultipartUpload records itself on the
  meta object before its head write", under Coexistence obligations). An
  abort that then removes a version no link of which landed reads its
  instance entry once more, after the removal's prepare, and again once it
  canceled an op on it past the tag timeout, and compares the stored bytes
  that `bi_get` answers in the op that removes the entry
  ("AbortMultipartUpload leaves a completed upload's parts alone").
  `bi_put`, `bi_list` and the repair paths stay out of scope.

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
no restore, answers the 403 ("Object read differences", "Cloud
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
   radosgw stores ("A PutBucketEncryption document is held to S3's
   schema and AWS's values"), and GetBucketEncryption writes a bucket
   key as `true` where radosgw writes `True` ("GetBucketEncryption
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
19.2.7 included (Tentacle fixed it in 20.2.3), lists the queue registry
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
  example the user-stats reset in the user class and, on ceph main though
  in neither floor release, the OLH link and instance-unlink methods, which
  answer the epoch they used
  ([cls_rgw.cc:1927-1929](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/cls/rgw/cls_rgw.cc#L1927-L1929),
  [cls_rgw.cc:2173-2176](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/cls/rgw/cls_rgw.cc#L2173-L2176), at
  d8552606423; "OLH epochs differ by OSD release"), return their data only when the
  operation carries the return-vector flag, which the librados C API
  exposes for read-op operate but not for write-op exec, whose signature
  has no output buffer. The OSD classifies a class call as a write from
  the method's own registration flags, not from the request, which is
  why a read op can carry such a call. This is a go-ceph fork item, and
  it gates persistent notifications rather than a corner case: the 2pc
  queue reserve returns the reservation id in a write op's reply, and
  radosgw runs it with the return-vector flag on every persistent event.
- **OLH epochs differ by OSD release.** Squid's and Tentacle's cls_rgw
  take an OLH link or unlink whose epoch is 0 as the name's next epoch, 2
  for its first, 1 being a converted plain entry's
  ([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517),
  [cls_rgw.cc:1630-1644](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644)), and
  apply a named epoch below the entry's without moving the current
  version: the link writes the version non-current, the unlink removes its
  list entry and logs its removal
  ([cls_rgw.cc:1751-1759](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1759),
  [cls_rgw.cc:1917-1928](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1917-L1928);
  [cls_rgw.cc:1892-1901](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901),
  [cls_rgw.cc:2064-2076](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2064-L2076)). And
  neither release's link or unlink answers anything
  ([cls_rgw.cc:1652-1865](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1652-L1865),
  [cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018);
  [cls_rgw.cc:1781-2000](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1781-L2000),
  [cls_rgw.cc:2002-2159](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159)). rgw-go
  sends epoch 0 in every link and unlink an S3 request makes, as radosgw
  does for a requester that is not a system user
  ([rgw_op.h:2117-2143](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L2117-L2143),
  [rgw_op.h:2280-2306](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L2280-L2306)), and decodes no
  epoch reply on either floor. Only a request that keeps a version's own
  epoch names one: a lifecycle transition's link, and the link that puts
  back a version whose removal was refused. The class on main takes the
  time in nanoseconds for an epoch of 0 and answers the epoch it used
  ([cls_rgw.cc:1865](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/cls/rgw/cls_rgw.cc#L1865),
  [cls_rgw.cc:1927-1929](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/cls/rgw/cls_rgw.cc#L1927-L1929),
  [cls_rgw.cc:2173-2176](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/cls/rgw/cls_rgw.cc#L2173-L2176), at
  d8552606423), which neither floor carries.
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
  has to be unique within one client. Two consequences follow. A restarted
  gateway cannot renew or release its predecessor's lock even with the
  same cookie, so recovery is by expiry, as radosgw's is. And a lock on a
  missing object creates it, so wherever the object's absence matters
  radosgw prepends an existence assertion, and rgw-go does the same.
  radosgw's multipart serializer never sets a cookie and locks under ""
  (`cls_lock_client.h:79`; `MPRadosSerializer`,
  `driver/rados/rgw_sal_rados.h:848-860` at v19.2.6, `:905-916` at
  v20.2.4), which is safe there because it unlocks only a hold its own
  lock's reply confirmed. Every request of an rgw-go gateway is one RADOS
  client, so each multipart completion and abort takes the
  `RGWCompleteMultipart` lock, renews it, asserts it and releases it under
  a cookie of its own, 128 random bits (`newLockCookie`,
  `internal/driver/mp_cleanup.go`): an unlock or a renewal acts on that
  attempt's hold alone and answers ENOENT for another's
  (`remove_lock` and `lock_obj`, `cls/lock/cls_lock.cc:282-304` and
  `:190-199` at v19.2.6 and v20.2.4). A radosgw gateway is another client,
  so the cookie changes nothing in how its locks and rgw-go's exclude each
  other.
  The class's failure modes matter for renewal: a renew with
  `LOCK_FLAG_MUST_RENEW` by a client that does not hold the lock fails
  with `ENOENT`, not `EBUSY` (`MAY_RENEW` gets `EBUSY`), which radosgw's
  reshard renewal reads as expiry; and an expired ephemeral lock's object is
  removed only by a lock call that succeeds, because unlock, break,
  get_info and assert_locked calls that fail drop their transaction and
  leave it in place.
- **A CompleteMultipartUpload records itself on the meta object before its
  head write.** rgw-go writes the completion record of
  [ceph/ceph#72103](https://github.com/ceph/ceph/pull/72103), the fix in
  review for tracker [#80896](https://tracker.ceph.com/issues/80896), in
  that PR's format as of its head, 2c1db8239cc (2026-09-28), so that a
  radosgw carrying the fix and rgw-go read each other's records:
  `user.rgw.mp_completion_tag`, the idtag the completion's head carries
  with its trailing NUL, and `user.rgw.mp_completion_instance`, the
  instance of the version the completion writes, its key's on a bucket
  whose versioning is enabled, and `null` for the key's own head. Both go
  on the meta object in the op that renews the completion lock, before the
  head write, and both are removed after a head write that certainly wrote
  nothing. After one that timed out they stay, and so they do once a head
  landed, whatever its completion or its link then answered, where the PR
  removes them after any failure but a timeout: that head names the parts,
  and a removal of its version queues them for the GC
  (`docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload leaves
  its upload whole when its head lands and write_meta
  fails](ceph-upstream-bugs.md#radosgws-completemultipartupload-leaves-its-upload-whole-when-its-head-lands-and-write_meta-fails)").
  A completion that finds a record finishes the earlier completion, writing
  no head, only when the head the record names, the recorded version read
  without following the key's OLH, carries the recorded tag and the ETag
  the request's parts make, and answers with that version's id; otherwise
  it answers 404 NoSuchUpload and writes nothing, because that head was
  never written or was replaced or deleted and its parts queued for the GC.
  A retry after a crash between the record and the head write, or after a
  head write that timed out and never landed, is therefore refused, and the
  upload has to be aborted, as in the PR. A record of a version, an
  instance or the null version on its key's OLH head, is finished only once
  that version is linked. rgw-go reads the version's index entry and, when
  that entry is the completion's, the list entry at its versioned epoch: the
  version is linked only when that list entry carries the completion's tag,
  as only a link writes list entries and complete_op keeps the epoch of the
  entry it rewrites, which for a null version written over an earlier one
  is the earlier one's. When no link of it landed, after a link that timed
  out or a gateway that stopped once the head was written, rgw-go completes
  the entry such a gateway left pending and links the version before it
  answers 200: an instance under the retry's If-Match and If-None-Match,
  and the null version unconditionally, as the null write's own link is
  (`linkCond`, `internal/driver/put_versioned.go`). When the link cannot be
  established safely, because the version's head or its index entry is
  gone, the entry is another write's, or the link is refused, rgw-go
  answers 500 InternalError and keeps the meta object and the version, so a
  later retry may link it; a condition that no longer holds answers 412.
  An abort is the exit that leaves nothing behind: it removes such an
  unlinked version, its instance entry and then its head, its parts going
  to the GC with the upload's, once no link of it can still land and no
  other op on its index entry is younger than the bucket index's tag
  timeout, 120 seconds by default, and until then answers 500 too
  ("AbortMultipartUpload leaves a completed upload's parts alone"). The
  one exception is an unlinked null version, which keeps
  its parts and its data under its key's OLH head, the abort removing the
  meta object alone, as that exclusion states. The PR's replay answers 200
  with the recorded instance
  without reading its link (`driver/rados/rgw_sal_rados.cc:4632-4664` at
  2c1db8239cc), so a version whose link never landed is answered as
  written though its key's OLH does not name it. A
  retry of a completion into a version that its link's refusal, or the
  check after its link that voided a conditional completion with 412,
  removed with its parts therefore answers 500, and the upload has to be
  aborted: S3 lets an upload whose completion its condition refused be
  completed again, and rgw-go does when the condition refused it before its
  head write. A floor radosgw coexists with the record without knowing it:
  - it reads a meta object's `user.rgw.*` xattrs, the record's among them
    (`raw_obj_stat`'s `rgw_filter_attrset`, `driver/rados/rgw_rados.cc:8866`
    at v19.2.6, `:9810` at v20.2.4), and no code at either tag names them;
  - ListParts reads only named attrs among them: the ACL
    (`rgw_op.cc:6669-6694` at v19.2.6), and at v20.2.4 the ACL and
    `user.rgw.cksum` (`:7593-7633`); ListMultipartUploads reads the bucket
    index, and an abort removes the record with the meta object;
  - a completion copies the meta object's attrs onto the head
    (`rgw_op.cc:6467` at v19.2.6, `:7293` and `:7343` at v20.2.4), so an
    object a floor radosgw completes from a recorded upload carries the
    record, which nothing reads from a head and a GET does not render (it
    renders `rgw_to_http_attrs` and `x-amz-meta-` attrs alone,
    `rgw_rest_s3.cc:546-582` at v19.2.6, `:663-699` at v20.2.4). rgw-go
    strips the record from the attrs it copies.

  A floor radosgw writes no record, and answers neither refusal: a floor
  radosgw's retry or abort of an upload rgw-go recorded still meets
  #80896. The format is revisited when the PR merges.
- **Multipart uploads in flight are shared.** Either gateway may add parts
  to, list, complete or abort an upload the other began. What they share is
  radosgw's: the meta object's name and pool, `<key>.<upload id>.meta` in
  the `multipart` namespace of the placement's data-extra pool; its
  `multipart_upload_info` data and its omap of `part.%08d` part records;
  the part names; each part record's `past_prefixes`, the history of a part
  uploaded again; the index entries in the `multipart` namespace; and the
  `RGWCompleteMultipart` lock ("Lock names are radosgw's; cookies are not
  shared"). rgw-go writes the meta object and the part heads without an
  idtag, as radosgw's non-atomic writes do. On Tentacle they also share the
  upload's checksum type and flags in its `multipart_upload_info` and each
  part's checksum in its part record, so either gateway may extend an
  upload the other began, except one a Tentacle radosgw began with xxh3 or
  blake3, whose parts, copies and completion rgw-go answers 501 ("A
  checksum algorithm rgw-go does not compute is refused at write"). Their
  types must agree: a completion of an upload with a checksum type,
  through either gateway, refuses a part without a checksum or with one of
  another type with 400 InvalidRequest, as radosgw's does
  ([rgw_op.cc:7105-7131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7105-L7131);
  "CompleteMultipartUpload combines the parts it completes and compares the
  whole supplied checksum"). One cost is accepted: a part without a
  manifest, which only a gateway older than Hammer wrote, is skipped by
  rgw-go's abort, and its objects leak on both gateways
  ("AbortMultipartUpload removes the upload before it frees the parts").
- **Signature quirks are radosgw's, not AWS's.** When a request carries
  no `x-amz-content-sha256` header, radosgw treats the payload as
  unsigned rather than rejecting the request as AWS does. This is a hard
  requirement, not a preference: Rook's own admin client, go-ceph's
  rgw/admin package, signs with the unsigned-payload hash and never sends
  the header, so it works against radosgw only because of that fallback.
  rgw-rs's spike chose AWS's behavior; rgw-go must not.
- **Admin responses carry a Content-Type on every body.** rgw-go renders
  the admin API in JSON, XML or HTML exactly as radosgw's formatters do,
  bodies byte for byte, with radosgw's Content-Length and Accept-Ranges
  headers, but it sets a Content-Type on every body, the format's own or,
  for the realm, realm-list and period getters, the `application/json`
  radosgw names there whatever the format; radosgw sends none on a JSON
  body its flusher started or on the zone configuration. radosgw also
  fixes a 200 status as soon as an op starts its body, and an op that
  fails after that still answers 200 with its body cut short: a bucket
  listing for a uid that does not exist gets a 200 with no document, a
  bucket index check given `check-objects` without `fix` a 200 that stops
  after the multipart entries, one whose index read or entry removal
  fails a 200 cut short there, and a usage or user listing whose RADOS
  read fails midway a partial one. rgw-go renders the whole body first and
  answers each of these with the error document and its status. It also
  answers the failure of an index check's object listing, a page radosgw
  lists again for as long as it fails, holding the request for good when
  the failure persists ([radosgw's bucket check object listing retries a failed page without
  end](ceph-upstream-bugs.md#radosgws-bucket-check-object-listing-retries-a-failed-page-without-end)). A
  metadata listing whose RADOS read fails after its first page answers
  the error status, but radosgw writes the error document into the
  formatter that holds the keys listed so far, so its body is those keys
  with the error document after them and the listing's sections left open
  (`RGWOp_Metadata_List::execute` and `end_header`, `rgw_rest_metadata.cc:134-158`
  and `rgw_rest.cc:620-624` at v19.2.6); rgw-go sends the error document
  alone. Clients
  that read the body, go-ceph and Rook among them, see no difference on
  success; one that inspects the headers does.
- **A purging bucket removal checks the index is empty at the end.**
  Removing a bucket with purge-objects, or a user or account with
  purge-data, deletes every listed object and then removes the bucket
  through the same path as S3 DeleteBucket, which refuses a bucket that
  still holds an object. radosgw removes the bucket at that point without
  checking, so an object written between its listing and the removal is
  left without a bucket; rgw-go answers 409 BucketNotEmpty instead, and a
  retry purges it.
- **The admin user routes answer store failures, and keep the account
  users index clean where radosgw leaves entries behind.** On
  `/admin/user` create, info, modify and remove, rgw-go differs from
  radosgw's `RGWUser` paths (`driver/rados/rgw_user.cc` at v19.2.6 and
  v20.2.4) in eight ways:
  - A failed read of a user, of the email index or of the access key
    index fails the request. radosgw's `RGWUser::init` and its duplicate
    checks count any failure as "no such user", so a RADOS error there
    makes radosgw answer NoSuchUser, skip a duplicate check, or go on to
    create the user.
  - Removing an account's root user also removes its display name from
    the account's users index, which radosgw leaves behind
    (docs/ceph-upstream-bugs.md, "Removing an account's root user leaves
    its name in the account's users index").
  - A write of an account member adds the user's users index entry
    before it stores the user, whether or not the account, the path or
    the display name changed, and removes the old entry after it, only
    when the display name's lower-cased key changed. radosgw links the
    user after storing it and only when the link changed, so a write that
    stops between leaves a member without an entry for good, and an
    account removal then succeeds while the user names the account
    (docs/ceph-upstream-bugs.md, "radosgw's user write can leave an
    account member out of the account's users index"). A write that stops
    in rgw-go leaves at most an entry naming the user under its old name.
    On a zone shared with radosgw, that entry blocks the old display name
    for any other user radosgw stores in the account, and adds one to
    `count_account_users` toward `max_users`, for as long as the entry
    lasts, which the member's removal does not end; while the member
    lives, IAM ListUsers lists it twice (`services/svc_user_rados.cc:283-290`
    at v19.2.6 and `:260-267` at v20.2.4, `rgw_rest_iam_user.cc:175-189`,
    and `driver/rados/rgw_sal_rados.cc:1426-1429` and `:1966-1969`, which
    skip a gone user). rgw-go's display name check compares each entry's
    user's current display name, so the entry blocks no name there, and
    rgw-go counts no entries toward a limit.
    Every write of an account member reads the account's users for the
    display name check, which radosgw does only when the link changed.
  - A user removal removes the user before its users index entry, where
    radosgw unlinks the entry first (`services/svc_user_rados.cc:601-610`
    and `:624` at v19.2.6, `:575-584` and `:598` at v20.2.4). A removal
    that stops between leaves only an entry naming a gone user, which the
    account's removal and the display name check skip.
  - Removing an account user whose users index entry is already gone,
    root or not, completes. radosgw answers NoSuchUser for a non-root user
    without its entry, for good, and takes a removal whose uid object lost
    a version race for success with the user left in place; rgw-go answers
    that race 409 ConcurrentModification and removes the user on retry
    (docs/ceph-upstream-bugs.md, "radosgw cannot remove an account user
    whose users index entry is gone").
  - radosgw refuses a display name in an account when the users index
    entry under that name names another user, even one since removed.
    rgw-go reads the account's users and refuses a name only a live user
    of the account holds, so a name a stale entry holds is accepted and
    the entry rewritten.
  - Removing a user with purge-data leaves a bucket the user's bucket list
    names when the bucket now belongs to another owner, where radosgw
    removes the listed bucket whoever owns it.
  - radosgw writes a user's email index after the user, over whatever it
    holds unless the write is exclusive, so two concurrent writes giving
    one email to two users both store it and the index names the last, and
    a user's write takes an account's email from the account
    (docs/ceph-upstream-bugs.md, "radosgw lets a user take an account's
    email and deletes it with the account"). rgw-go claims a new email's
    index object before it writes the user, creating it exclusively: an
    index naming the user is its own, left by a write that stopped before
    the user, and one naming another user or an account answers 409
    EmailExists and stores nothing, whether or not that user still holds
    the email. With `rgw_user_unique_email` false, which lets users share
    an email, a user create or modify leaves another user's index to be
    written over after the user, as radosgw writes it, under the version
    its claim read: an index changed since is claimed again, and one an
    account took meanwhile is left, which answers 409 EmailExists once the
    user, its key indexes and its releases are written, the email
    unindexed; an index radosgw wrote, without
    a version, is written over unchecked; an account's email
    answers 409 whatever the option says, and a metadata put refuses
    another user's email whatever it says. The index is written with a
    version, and a user's removal or change of email removes it only while
    it names that user, under the version read, where radosgw removes it
    whoever it names; an index radosgw wrote, which has no version, is
    removed once it is read to name the user. So a departing sharer never
    removes another sharer's index or an account's, but it does leave the
    other sharers unindexed when the index named it: they still hold the
    email, no lookup by email finds them, and an account created then may
    take the email, after which an account and a user hold it. The claim
    fences rgw-go writers; a radosgw gateway writes over it. An index naming a user that no longer
    holds the email, one claimed by a write that then failed or an old
    email's whose removal a write stopped before, refuses the email to
    every other user until a write of that user takes the email and a later
    one drops it. A refused email claim leaves the write's key claims in
    place, as any failed write does. The memstore refuses the same emails
    under its lock.
- **The admin key, subuser, caps and quota routes differ from radosgw in
  seven ways.** On `/admin/user?key`, `?subuser`, `?caps` and `?quota`
  (`driver/rados/rgw_rest_user.cc` and `driver/rados/rgw_user.cc` at
  v19.2.6 and v20.2.4), and, for the key checks, on `/admin/user` create
  and modify, which add keys through the same code:
  - A failed read of a user or of the access key index fails the request,
    as on the other user routes; that includes `generate_key`'s check that
    no other user holds an access key the request names, which radosgw
    passes on any failure (`rgw_user.cc:572-575` at v19.2.6, `:577-580` at
    v20.2.4).
  - No user is found and no key checked through the Swift key index, which
    rgw-go's user store does not read. radosgw's `RGWUser::init` finds the
    user by a Swift key when the request names `key-type=swift`, an access
    key and no uid of an existing user (`rgw_user.cc:1427-1430` at v19.2.6,
    `:1432-1435` at v20.2.4); rgw-go answers such a request as one that
    names no user, 400 InvalidArgument without a uid and 404 NoSuchUser
    with one. radosgw also refuses with 409 KeyExists a Swift key create
    whose `access-key` another user holds as a Swift key, an id it then
    discards for `<uid>:<subuser>` (`:566-569`; `:571-574`); rgw-go does
    not check it. A `<uid>:<subuser>` another user holds is refused with
    radosgw's 409 KeyExists by the user store's write.
  - A key change modifies the stored key in place. radosgw rebuilds a
    Swift key from its id and subuser alone, so rotating its secret
    through the subuser routes, or creating the subuser again, makes a
    deactivated key active and drops its creation date, and a key create
    with `generate-key=false` and no `secret-key` stores it without a
    secret (docs/ceph-upstream-bugs.md, "radosgw's Swift key modify
    rebuilds the key"). rgw-go keeps the key's active flag, which changes
    only when the request names `active`, and its creation date, and
    answers 400 InvalidSecretKey, radosgw's answer to an empty secret on
    a new key (`rgw_user.cc:589-592` at v19.2.6, `:594-597` at v20.2.4),
    to any change that would leave a key without a secret, storing
    nothing.
  - radosgw checks an access key id another user holds only through its
    key index, which lists active keys, so it lets a second user take a
    deactivated key's id (docs/ceph-upstream-bugs.md, "radosgw lets
    another user take an inactive access key's id"). rgw-go keeps a key
    holder index beside radosgw's key index: an object per access key id,
    active or not, naming the user holding it, in the key index's pool
    under the key index's namespace with `.rgw-go-key-holders` appended
    (`users.keys.rgw-go-key-holders` in a default zone), which radosgw
    neither reads nor writes. Before it writes the user, every user write
    claims each access key it gains, one the stored user lacks or one it
    makes active: an entry naming the user is moved on a version under the
    version read, a missing one is created exclusively, and one naming
    another user answers 409 KeyExists, storing nothing, so of two
    concurrent rgw-go writes giving one id to two users, one fails. A key
    the write keeps or deactivates is claimed the same way but best effort,
    with no search outside the index: any failure to read, move or create
    its entry is logged by the index's kind and the write goes on, so a
    revocation, a suspension included, never fails or waits on the index,
    and a write that keeps a key, and a key removal, are never refused.
    Each key a write drops is released after the user is written, and a
    removed user's keys once the user is gone; a release removes the entry
    under the version it read, so a write of the user that moves or
    creates the entry after the release read it keeps it. A write whose
    move lands before the release reads the entry does not: the release,
    of a write dropping the key, removes the entry the other write moved,
    and another user's claim, finding no entry and, before that write's
    user lands, no holder, can then take the id, after which both users
    hold it. A write that gains a key reads its entry again once it has
    written the user and creates it exclusively when it is gone, which
    narrows that to another user's claim landing between the release and
    the re-read; an entry naming another user then is logged and the write
    stands. An entry the re-read creates is released again when the user,
    read once more, no longer holds the key, as a later write of the user
    that dropped it, whose release came first, leaves it. The admin routes refuse the same
    ids before they write anything (`refuseHeldKey`,
    `internal/op/adminuser_sub.go`): a key create, a subuser create, a user
    create or modify and a metadata put naming a key, and a key modify
    whose result is active. The memstore keeps the same index under its
    lock.
    A user radosgw or radosgw-admin wrote, or rgw-go wrote before the
    index, has no entries, so a key a write gains that the index holds no
    entry for is looked for outside it: in radosgw's key index, then in
    every stored user, one read per user, every page of the user listing.
    Another user found holding the key, active or not, refuses the write as
    an entry would, and any failure to read the listing or a listed user
    refuses it too; only a user gone between the listing and its read is
    skipped. So the first rgw-go write gaining a key id no entry records
    reads every user, as the routes' check did before the index; a user's
    own next write claims its keys, after which a lookup is one read. The
    index fences rgw-go writers only: radosgw never reads it, so a radosgw
    gateway can give a held inactive id to a second user at any time, not
    only while an rgw-go write is between its lookup and its create, and
    writes no entry for it, after which both users hold the id. An entry is
    never taken from the user it names, and never released on a failure: a
    claim outlives a write that fails after it, whether the write stops or
    answers an error, a lost race's 409 UserAlreadyExists or
    ConcurrentModification and a failed index write among them, since a
    release could not tell a failed write from one about to be retried. Such a write, a write that stops after the user and before its
    releases, and a removal that stops before its releases, leave entries
    naming a user that does not hold the key, and that key's id stays
    refused to every other user. A retry of a write that stopped before the
    user completes it; an entry a failed write, a release or a removal left
    stays until a write of that user holding the key and a later one
    dropping it, or a user created again under that id holding the key and
    then removed, frees it. radosgw and radosgw-admin never touch the
    index either, so a user, or a user's key, that either removes keeps its
    entries, the likeliest source on a zone the two share: an rgw-go write
    giving that key id to another user answers 409 KeyExists, for good,
    where radosgw stores it. rgw-go never frees such an entry itself, since
    nothing it reads tells a user gone for good from one about to hold the
    key again. An operator who has checked that the user the entry names,
    the uid in its data (`rados get`), no longer holds the key frees the id
    by removing the entry, named by the access key id in the pool of the
    zone's `user_keys_pool` (`radosgw-admin zone get`) under its namespace
    with `.rgw-go-key-holders` appended: `rados -p default.rgw.meta -N
    users.keys.rgw-go-key-holders rm <access key id>` in a default zone.
    A removal outside rgw-go sends no cache notify, so an rgw-go gateway
    that cached the entry, on its first read of it after a miss, less than
    `rgw_cache_expiry_interval` ago (900 s by default) refuses the id until
    that copy expires, which later reads do not postpone, or is evicted, or
    the gateway restarts.
    Errors and logs name an entry by the index's pool and kind, `user key
    holder index`, never by the key.
  - A new key gets the `active` the request names. radosgw's
    `generate_key` never reads it and makes every new key active
    (`rgw_user.cc:536-639` at v19.2.6, `:541-644` at v20.2.4), so
    `active=false` on a new key is ignored there.
  - A quota set by arguments answers 400 InvalidArgument and stores
    nothing when `max-objects`, `max-size` or `max-size-kb` does not
    parse, when `max-size-kb` in bytes does not fit in 64 bits, and when
    the maximum size it would store is negative other than -1, radosgw's
    "no limit", so `max-size-kb=-1`, which stores -1024 bytes, answers
    400 InvalidArgument: use `max-size=-1` for no limit. The rule applies
    to the size the request leaves in place too: on a user whose stored
    `max_size` is negative other than -1, as radosgw stores for
    `max-size-kb=-1`, every later quota set that gives arguments but no
    `max-size` or `max-size-kb`, `max-objects=100` alone among them,
    answers 400 until the request also sets the size. A quota body is held
    to the same two size rules. radosgw
    stores -1, no limit, for an unparsable `max-objects` or `max-size`, an
    uninitialized value times 1024 for an unparsable `max-size-kb`, the
    wrapped product of an overflowing one, and any negative size, which
    it enforces as no limit (`rgw_quota.cc:775` and `:820` at v19.2.6,
    `:796` and `:841` at v20.2.4); each answers 200
    (docs/ceph-upstream-bugs.md, "radosgw's quota set stores garbage for
    an unparsable max-size-kb").
  - A quota set's JSON body is read with Go's JSON grammar, where radosgw
    uses json_spirit's (`src/json_spirit` at both tags), which also takes
    a stray comma in an object or an array and C white space such as a
    form feed; rgw-go answers such a body 400 InvalidArgument. Both read
    only the body's first value, and radosgw takes a lone number, `true`,
    `false` or `null` only when it is the whole body as json_spirit writes
    it back: rgw-go compares the body as sent, so it takes `1.0` or `-0`,
    which radosgw refuses.
- **The admin user routes refuse a boolean argument they cannot parse.**
  On `/admin/user` create, modify and remove and on the key, subuser and
  quota routes, a value for `generate-key`, `active`, `suspended`,
  `system`, `account-root`, `exclusive`, `purge-data`, `generate-secret`,
  `gen-access-key`, `purge-keys` or `enabled` other than an empty value,
  `true` or `false` in any case, `1` or `0` answers 400 InvalidArgument
  after the cap check, and nothing is stored; so does an empty `active=`.
  radosgw's `RESTArgs::get_bool` writes the caller's default for such a
  value and returns an error the bodies discard, and reads an empty
  value as true (`rgw_rest.cc:1002-1032` at v19.2.6, `:1007-1037` at
  v20.2.4), so `active=flase` or `active=` on a key create makes the key
  active (docs/ceph-upstream-bugs.md, "radosgw's admin API takes an
  unparsable boolean argument as its default"). Other flags keep
  radosgw's reading of an empty value as true. User info's read-only
  `stats` and `sync` keep radosgw's reading.
- **The admin user document withholds the Swift TempURL keys with the
  other keys.** radosgw's user document leaves out `keys` and
  `swift_keys` for a caller holding only `user-info-without-keys=read`
  but still writes `temp_url_keys`, the TempURL signing secrets
  (`driver/rados/rgw_user.cc:145-148` and `:162` at v19.2.6, `:150-153`
  and `:167` at v20.2.4; docs/ceph-upstream-bugs.md, "radosgw's admin user
  info shows the Swift TempURL keys to a caller it withholds keys from").
  rgw-go leaves `temp_url_keys` out too, so such a caller's document lacks
  the section; a caller that may see keys gets radosgw's document.
- **The admin bucket info reports a bucket named tenant/name whatever the
  uid's tenant.** `GET /admin/bucket?bucket=tenant/name` loads the tenanted
  bucket in both. radosgw's `bucket_stats` then looks the whole name up
  again under the uid's tenant (`driver/rados/rgw_bucket.cc:1655` at
  v19.2.6, `:1821` at v20.2.4). Without a uid, or with one in the empty
  tenant, that is the same entry point, as `rgw_bucket::get_key` keys
  `("", "t/n")` and `("t", "n")` alike (`rgw_basic_types.cc:62-79` at
  both tags), so radosgw reports the bucket, as it reports every tenanted
  bucket in `GET /admin/bucket?stats=true`. With a uid that names a tenant,
  such as `uid=t2$u`, radosgw finds nothing after its flusher has started
  and answers 200 with no document (docs/ceph-upstream-bugs.md, "radosgw's
  admin bucket info answers nothing for a tenant/name bucket when the uid
  names a tenant"); rgw-go reports the bucket it loaded.
- **Bucket link and unlink write differently from radosgw.** On `PUT
  /admin/bucket` (link, with or without `new-bucket-name`), `POST
  /admin/bucket` (unlink) and the bucket adoption a user's move into an
  account runs (`RGWBucketAdminOp::link` and `::unlink`,
  `driver/rados/rgw_bucket.cc:999-1187` at v19.2.6, `:1150-1338` at
  v20.2.4; `RadosBucket::chown`, `driver/rados/rgw_sal_rados.cc:699-751`
  at v19.2.6, `:717-769` at v20.2.4), rgw-go keeps to one rule: no failure
  of an rgw-go request between two of its writes, followed by any later
  rgw-go admin or S3 request, leaves two owners reaching one bucket's data,
  a live bucket without its name, or an instance object carrying a live
  bucket's id that no entry point names. Only while a rename or a link of
  a bucket is unfinished can its owner's list lack it. An unfinished
  rename can also leave its old owner's list naming it, which grants no
  access; the rename's retry, the one change the bucket then admits, puts
  both lists right. A link without a rename that fails at the instance
  write after its unlink leaves the bucket its old owner's, in neither
  list and with no record, as radosgw's own link does; a retry of that
  link, or any other link of the bucket, puts it in its owner's list. And
  no rgw-go link names an instance whose id another entry point already
  loads, so what radosgw's own failed rename leaves is not turned into a
  second owner either.
  - A link whose bucket ends under a name another bucket's entry point
    holds answers 409 BucketAlreadyExists before writing anything: a
    rename onto that name, a link across tenants, and a link by
    `bucket-id` of an instance whose name now belongs to a re-created
    bucket. radosgw overwrites that entry point (docs/ceph-upstream-bugs.md,
    "radosgw's bucket link overwrites the entry point of the bucket it
    renames onto" and
    "radosgw's bucket link by bucket id takes the name from the live
    bucket"). The entry point a link writes is written under the version
    it read, or created exclusively when there was none; radosgw writes it
    unchecked. A link by `bucket-id` answers a wrong id 404 NoSuchKey, as
    radosgw does.
  - A link by `bucket-id` first reads every entry point of the bucket
    metadata section, page by page, and answers 409 BucketAlreadyExists
    when one under another name loads the bucket's id, and 409
    ConcurrentModification when a listed entry point loads nothing; a
    listing or a read that fails answers the store's error, 500
    InternalError; a listing that reports the section missing answers 404
    NotFound, and one that makes no progress 500 InternalError: radosgw's
    own failed rename leaves an instance carrying the live bucket's id that
    no entry point names, which radosgw's link by `bucket-id` names, giving one bucket's data two owners
    (docs/ceph-upstream-bugs.md, "radosgw's bucket link by bucket id takes
    the name from the live bucket"). Each such link reads every bucket. One
    unfinished rename whose new name's entry point names an instance not
    yet written makes that listed name load nothing, so every by-id link of
    the zone answers 409 until that rename is retried, which fails closed:
    retry the rename, then the link.
  - A rename records itself in the bucket's instances while it runs, in an
    xattr of radosgw's attr namespace, `user.rgw.rgw-go-rename`, which
    radosgw carries and never reads; each record an attempt writes also
    holds the RADOS version that attempt leaves on each of the two
    instances. Until the rename completes, rgw-go
    answers every other link, unlink or chown of the bucket 409
    ConcurrentModification, and so do an S3 DeleteBucket and an admin
    bucket removal, with or without `purge-objects` or `bypass-gc`, by
    either name, before they delete anything, as both names share the
    bucket's index. Only a retry of the same rename, by the old name, or by
    the new one once the old name is gone, goes on, and completes it. When
    a bucket has since been created under the old name, the identical retry
    loads that bucket and answers 409 BucketAlreadyExists; only a retry
    through the new name, `bucket=<new name>` with the same uid and no
    `new-bucket-name`, completes the rename then. A record whose new name
    another bucket took is void. radosgw and radosgw-admin do not honor the
    record: a radosgw or radosgw-admin link of the bucket while an rgw-go
    rename is unfinished can still split it between two owners, and a
    radosgw or radosgw-admin removal of either name, `radosgw-admin bucket
    rm` or radosgw's S3 DeleteBucket, purges the index both names share.
    Retry a failed rgw-go rename before running radosgw or radosgw-admin on
    that bucket.
  - A removal and a rename of one bucket never both change it. The RADOS
    driver's removal starts by rewriting the bucket's instance, under the
    version it read and only while no live rename record is there, with an
    xattr `user.rgw.rgw-go-removing`: a rename that read the instance
    before fails its guarded write of the record, having changed nothing
    of the bucket, a rename that reads it after refuses the bucket with 409
    ConcurrentModification, and a rename record written first fails the
    removal's write and is then found. A purge, by `purge-objects` or
    `bypass-gc`, deletes the bucket's objects before that first write, so a
    rename that records itself during the purge goes on with the objects
    the purge deleted gone, and the removal then refuses. Each removal
    writes a value of its own into the xattr. A removal that loses the
    bucket's entry point to another write after its first write answers
    409 ConcurrentModification, an S3 DeleteBucket included, as the bucket
    stays, and clears the xattr under the version it wrote; when the
    winner rewrote the instance too, as a link or a chown does, it reads
    the instance again and clears the xattr under that version while it
    still holds its own value, so the winner's write is kept and later
    renames are not refused. radosgw removes the bucket's entry point
    first and its owner's list entry last, and a removal that loses the
    entry point returns before anything else goes
    (`driver/rados/rgw_rados.cc:5279-5284` at v19.2.6, `:5993-5998` at
    v20.2.4; `driver/rados/rgw_sal_rados.cc:444` and `:461` at v19.2.6,
    `:462` and `:482` at v20.2.4), yet its S3 DeleteBucket answers that
    race 204 (`rgw_op.cc:3793-3796` at v19.2.6, `:4002-4005` at v20.2.4),
    having removed nothing (docs/ceph-upstream-bugs.md, "radosgw's bucket
    delete answers success when its instance removal loses a race"). A
    removal that fails otherwise after its first write leaves the xattr,
    and renames of the bucket are refused until the removal is retried.
    radosgw neither writes nor reads the xattr.
  - The rename writes in another order than radosgw: the new name's entry
    point is created first, which secures the name; the instance under the
    old name gets the new owner, a default ACL, or on a retry the ACL it
    holds, and the record, under the
    version read, so a rename that loses that write to a removal or to the
    freeing of its claim below has changed nothing of the bucket; the old
    owners' list entries go, those the record names too; the instance
    under the new name is written; the owner's list gains the new name;
    the old instance goes, then the old entry point, while it names the
    bucket and under the version read; last the record goes.
    radosgw writes the new instance before its entry point, gives the new
    owner only to the new instance and removes the old entry point before
    the old instance (docs/ceph-upstream-bugs.md, "radosgw's failed bucket
    rename leaves the old name to the old owner"). So every instance
    carrying the bucket's id is named by an entry point naming that id
    while it exists, and radosgw-admin's `bucket stale-instances rm` never
    meets one of rgw-go's (docs/ceph-upstream-bugs.md, "radosgw's
    stale-instance cleanup purges a live bucket's index"). A failure can
    leave the new name's entry point naming an instance not yet written, or
    the old name's naming one already removed; that name then loads nothing
    and a bucket cannot be created under it. A failure right after the
    first write leaves the bucket unchanged but the new name reserved that
    way. A retry of the rename completes either. Without one, `DELETE
    /admin/bucket?bucket=<name>` frees such a name: it removes an entry
    point naming an instance that does not exist, under the version read,
    and nothing else, unless the rename that reserved it, which the claim
    records in the new name's entry point, is live, which answers 409
    ConcurrentModification. It first rewrites that rename's old instance,
    when one is left, under the version read, so a retry of the rename that
    read it before fails, having changed nothing of the bucket. The old
    name's entry point carries no claim, so it is freed even while the
    rename's record is live: that is the rename's own next step, and a
    retry through the new name completes the rename. radosgw answers
    NoSuchBucket for such a name and frees nothing.
  - A rename that loses its new name to a bucket created after its check
    answers 409 BucketAlreadyExists having written nothing, and one that
    loses its first instance write answers 409 ConcurrentModification
    having written only its claim of the new name: the bucket
    keeps its old name, its owner, its ACL and its owner's list entry, and
    carries no record, so a later request is not refused.
  - A bucket configuration write made through either name while a rename
    is unfinished, such as a policy, ACL, tagging or versioning change, is
    never overwritten: a retry merges nothing, and refuses rather than copy
    over it.
    - Within one attempt, the rename removes the old instance only under
      the version its own mark wrote. When a write through the old name has
      changed it since, the attempt copies it whole to the new name and
      retries the removal, up to 20 times. Then it answers 409
      ConcurrentModification, with both names loading the bucket and the
      write in the old one. A write made through the new name after the
      attempt last wrote the new instance fails a later copy, and the
      attempt, the same way.
    - A retry, by either name, reads both instances from RADOS, past the
      metadata cache, before it writes anything. The cache can hold a copy
      another gateway changed until that gateway's notify arrives. When the
      instance the request loaded through the cache is no longer the one
      RADOS holds, the retry answers 409 ConcurrentModification having
      written nothing, and drops the cached copy so the next retry loads it
      afresh.
    - The retry then compares each instance's RADOS version with the
      versions the attempts' records hold. It goes on when one attempt's
      record names the versions both instances hold now, or when only one
      instance is there, as nothing can be lost then. Any other change to
      either instance since the last attempt left it answers 409
      ConcurrentModification having written nothing, with both names
      loading the bucket and each write where it was made. That covers a
      config write, an ACL or an attr through either name, one written and
      set back, and a concurrent retry's own write.
    - After such a refusal the operator reconciles the two names by writing
      the configuration the bucket should keep through both. A retry goes
      on once both instances have been written since every record and hold
      the same configuration (the info but for the name, and the attrs but
      for the record), as copying the one onto the other then changes
      nothing.
    - The retry writes each instance under the version it read, so a write
      made through either name during it fails the retry rather than being
      overwritten. A new instance a concurrent retry created since the read
      found none fails it too, where radosgw's exclusive write of the new
      instance takes that -EEXIST as success
      (`services/svc_bucket_sobj.cc:548-557` at v19.2.6, `:488-497` at
      v20.2.4).
    - radosgw checks nothing. It removes the old instance unchecked
      (`rgw_bucket.cc:1175-1179` at v19.2.6, `:1326-1330` at v20.2.4), so it
      answers a write made through the old name 200 and loses it. Its retry
      writes the new instance exclusively and, finding it, leaves it as it
      is (`rgw_bucket.cc:1138-1144` and `services/svc_bucket_sobj.cc:548-557`
      at v19.2.6, `:1289-1295` and `:488-497` at v20.2.4), so a write made
      through the old name between the attempts is lost too
      (docs/ceph-upstream-bugs.md, "radosgw's bucket rename loses a bucket
      config write made through the old name").
  - A bucket rename moves the lifecycle entry to the new name. An entry is
    keyed by the bucket's tenant, name and marker, and radosgw's rename
    moves none, so the renamed bucket's rules stop running
    (docs/ceph-upstream-bugs.md, "[radosgw's bucket rename leaves the
    renamed bucket out of the lifecycle
    index](ceph-upstream-bugs.md#radosgws-bucket-rename-leaves-the-renamed-bucket-out-of-the-lifecycle-index)").
    Once a rename of a bucket carrying `user.rgw.lc` completes, its retry
    included, rgw-go files the entry under the new name and removes the
    one under the old, logging a failure of either; a failure to file it
    leaves the bucket out of the index, as radosgw's rename does. An old
    entry left behind names a name that no longer loads the bucket, which
    the next lifecycle pass on its shard drops, radosgw's or rgw-go's, as
    for a delete ("A bucket delete removes the lifecycle entry once the
    bucket is gone"). A
    DeleteBucketLifecycle on the new name that lands between the rename's
    last instance write and its filing of the entry leaves an entry for a
    bucket without `user.rgw.lc`. radosgw's pass keeps such an entry and
    skips the bucket
    ([rgw_lc.cc:1776-1783](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1776-L1783)),
    which costs one bucket load per pass until the bucket's next lifecycle
    configuration put or delete, or its removal.
  - The owners' bucket list entries a link removes are the ACL owner's and
    the instance owner's when it differs, each only while it names the
    bucket's instance, and a failure is returned; radosgw removes the ACL
    owner's entry by name and logs a failure. An unlink removes the named
    owner's entry by name, as radosgw does, and returns a failure that
    radosgw logs and skips.
  - Linking a bucket to an account gives it the account's tenant; radosgw
    gives it the empty tenant, moving it out of the account's
    (docs/ceph-upstream-bugs.md, "radosgw's bucket link to an account
    moves the bucket out of the account's tenant").
  - The adoption takes only buckets the moving user holds. It loads the
    instance the owner's list entry names by its id, or, for an entry that
    carries no id, the bucket its name loads, as radosgw's `load_bucket`
    loads them (`rgw_sal_rados.cc:616-629` at v19.2.6, `:636-647` at
    v20.2.4), and skips one that is gone or that another owner holds. Each
    chown round then reads the bucket by its name and skips it when the
    name now loads another instance than the one loaded, or that instance
    under another owner than at the load; it writes the entry point under
    the version it read and starts the round over when another write moved
    it. A round skipped after an earlier one lost its race leaves that
    round's list writes: the user's entry removed and the account's entry
    added. When the skip is for another owner, the entry point names the
    account if that round got so far, and the instance, its ACL and its
    owner stay the other owner's; when it is for the name now loading
    another instance, the entry point is the other writer's and the loaded
    instance stays the moving user's, without her list entry. The adoption
    logs each such skip with the bucket's name and instance. So neither a
    stale entry whose name another owner re-created nor one that still
    names an instance a link gave to another owner, as a link whose
    removal of the old owner's entry failed leaves it
    (`rgw_bucket.cc:1118` and `:3424-3432` at v19.2.6, `:1269` and
    `:3510-3518` at v20.2.4, where radosgw logs the failure and goes on),
    hands that owner's bucket to the account. radosgw loads the bucket
    again in each round, chowns it from whoever holds it, and points the
    name at it unchecked (`driver/rados/buckets.cc:119-130` at v19.2.6 and
    v20.2.4; `rgw_user.cc:1681-1742` at v19.2.6, `:1687-1748` at v20.2.4;
    `RadosBucket::chown`, `rgw_sal_rados.cc:699-751` at v19.2.6, `:717-769`
    at v20.2.4). For an entry without an id, every round compares with the
    instance the first load returned, so a bucket re-created under the name
    between rounds is skipped, where radosgw loads the name again and
    adopts the new bucket. The chown's unlink removes the user's entry
    without an id with the rest, as radosgw unlinks by name.
- **The admin bucket routes refuse a boolean argument they cannot parse.**
  On `DELETE /admin/bucket`, a `purge-objects` or `bypass-gc` that is
  empty, bare, or other than `true` or `false` in any case, `1` or `0`
  answers 400 InvalidArgument after the cap check, and nothing is removed;
  so does such a `fix` on `GET /admin/bucket?index`, which removes index
  entries and rewrites the shard headers, and an unparsable `enabled` on
  `PUT /admin/bucket?quota`. radosgw takes an unparsable value as the
  default, false for the three removal flags and the current value for
  `enabled`, and an empty or bare one as true, so `purge-objects=` purges
  and `fix=` fixes (`driver/rados/rgw_rest_bucket.cc:232-233`, `:118` and
  `:319` at v19.2.6 and v20.2.4; docs/ceph-upstream-bugs.md, "radosgw's
  admin API takes an unparsable boolean argument as its default"). An
  empty `enabled=` reads as true, as in radosgw: it enables a limit. The
  bucket quota set holds the user quota set's rules for `max-objects`,
  `max-size` and `max-size-kb`: an unparsable value, a `max-size-kb`
  whose size in bytes overflows, and a maximum size below 0 other than -1,
  `max-size-kb=-1` among them, answer 400 and store nothing, with or
  without a body. The read-only `stats` of `GET /admin/bucket`, and the
  index check's `check-objects`, keep radosgw's reading.
- **The other admin bucket routes differ from radosgw in three ways.**
  - `DELETE /admin/bucket?object` and `GET /admin/bucket?policy&object=`
    answer 404 NoSuchKey for an object name no object can carry, empty,
    longer than 1024 bytes or not UTF-8, without reading RADOS. radosgw
    hands the name to RADOS, which answers ENOENT, radosgw's 404, for most
    such names and refuses one of over about 2000 bytes as too long. On a
    bucket whose versioning was ever enabled or that has object lock,
    `DELETE /admin/bucket?object` answers 501 instead, whatever the name
    ("The admin object removal refuses a bucket whose versioning was ever
    enabled").
  - `GET /admin/bucket` without a uid answers a failure to list the
    bucket metadata section with its error; radosgw ends its listing
    there and answers 200 with what it listed.
  - `DELETE /admin/bucket` refuses a bucket of another zonegroup with 301
    PermanentRedirect whatever the request carries. radosgw means to skip
    that check for a request another zone forwarded, which it recognizes
    by an `rgwx-zonegroup` argument its parser files where the check
    cannot see it (docs/ceph-upstream-bugs.md, "radosgw's admin bucket
    removal never recognizes a forwarded request"); rgw-go forwards
    nothing (Multisite).
- **The admin bucket removal with bypass-gc deletes every tail, and with a
  bound.** `DELETE /admin/bucket?bypass-gc=true` deletes the bucket's
  object data directly instead of queueing it for garbage collection, as
  radosgw's `remove_bypass_gc` does, with four differences. radosgw's
  admin op never sets that loop's concurrency, so its budget starts at 0:
  the first object whose manifest it walks keeps its tail stripes, neither
  deleted nor queued, and every later delete is issued without a bound
  ([`rgw_sal_rados.cc:505`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L505),
  [`:542-550`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L542-L550);
  `rgw_bucket.h:248`). rgw-go deletes every object's tails and keeps at
  most `rgw_gc_max_concurrent_io` deletes in flight, one when the option is
  0 or less. A tail stripe or a head that is already gone counts as
  deleted, as the garbage collector counts a stripe, so a retry of a
  removal that stopped partway completes. radosgw's removal stops on
  either, a missing stripe being tracker #24789: the admin op answers 404
  NoSuchBucket for a bucket that still exists, after deleting every head it
  reached, and a retry then removes the bucket. The tail deletes may use a
  full pool's reserve, as the garbage collector's do, so the removal can
  free space on a full cluster. And an object's head is removed only once
  every one of its tail deletes has succeeded, and a head removal that
  fails other than as
  missing or timed out cancels its index change: radosgw issues the head's
  removal and its index entry's removal alongside the tail deletes without
  waiting for them, so a failed delete there leaves stripes no head or
  index entry names, where rgw-go leaves the object listed for a retry to
  finish. Until that retry the object reads as damaged: its head is there,
  but the tails it names may already be released.
- **A purging bucket removal refuses a bucket whose versioning was ever
  enabled.** `DELETE /admin/bucket` with `purge-objects` or `bypass-gc`
  answers 501 NotImplemented for a bucket whose versioning is enabled or
  suspended before it deletes or aborts anything in it, until versioning is
  served (phase 2); radosgw purges the bucket's versions and delete
  markers. `DELETE /admin/user` with `purge-data` answers the same when it
  reaches such a bucket, with the user and that bucket left whole and the
  buckets it removed before then gone. A removal without a purge of an
  empty versioned bucket goes on as before. Both purges, and `DELETE
  /admin/user` with `purge-data`, also refuse a bucket whose flags carry
  object lock without `BUCKET_VERSIONED`, a state no radosgw op writes:
  bucket creation enables object lock only with versioning
  (`driver/rados/rgw_rados.cc:2392` at v19.2.6, `:2497` at v20.2.4), and
  `RGWPutBucketObjectLock` refuses a bucket whose versioning is not enabled
  (`rgw_op.cc:8232` at v19.2.6, `:9180` at v20.2.4). A `bucket.instance`
  metadata put stores the flags it is given, in radosgw
  (`driver/rados/rgw_bucket.cc:2803-2864` at v19.2.6, `:2921-2997` at
  v20.2.4) as in rgw-go, so such a bucket can still arrive. radosgw would
  purge it as an unversioned one, as `versioning_status` reads only the
  versioning flags (`rgw_common.h:1075` at v19.2.6, `:1117` at v20.2.4);
  rgw-go's purges refuse it before their first delete, taking it for a
  versioned bucket, though the object delete itself takes radosgw's
  unversioned path there.
- **The admin object removal refuses a bucket whose versioning was ever
  enabled.** `DELETE /admin/bucket?object` answers 501 NotImplemented for
  a bucket whose versioning is enabled or suspended, or that has object
  lock, and removes nothing, until versioning is served (phase 2). radosgw
  keys the object with no instance and passes the bucket's versioning to
  the delete (`RGWBucket::remove_object`,
  `driver/rados/rgw_bucket.cc:274-289` at v19.2.6, `:275-290` at v20.2.4;
  `RadosObject::delete_object`, `driver/rados/rgw_sal_rados.cc:2933-2937`
  at v19.2.6, `:3775-3779` at v20.2.4), which on such a bucket writes a
  delete marker whether or not the object exists
  (`driver/rados/rgw_rados.cc:5774-5812` at v19.2.6, `:6462-6517` at
  v20.2.4). With versioning enabled the marker takes a new instance and the
  object survives as a noncurrent version. With versioning suspended the
  marker is the null version, which replaces the pre-versioning object, the
  bucket's null version: `rgw_bucket_link_olh` unlinks that object's entry
  and logs its removal (`cls/rgw/cls_rgw.cc:1712-1725` and `:1809` at
  v19.2.6, `:1853-1866` and `:1950` at v20.2.4), and `apply_olh_log`
  removes it (`driver/rados/rgw_rados.cc:8283-8284` and `:8334-8345` at
  v19.2.6, `:9225-9226` and `:9276-9287` at v20.2.4), as S3 has it for a
  suspended bucket. The refusal is the route's own: the RADOS store's
  object delete takes radosgw's versioned path, writing a delete marker, on
  a bucket whose flags hold `BUCKET_VERSIONED`, and the unversioned path on
  any other. The refusal follows the bucket load and comes before the
  object-name check, as radosgw's versioned path answers no ENOENT for a
  missing object.
- **A purging removal resumes each page of a versioned bucket at its last
  key's remaining versions.** `DELETE /admin/bucket` with `purge-objects`
  or `bypass-gc`, and `DELETE /admin/user` with `purge-data`, list a bucket
  whose flags hold `BUCKET_VERSIONED` with versions a page at a time,
  remove each entry of the page, and list the next page from the first
  version left of the key the page ended on, not after the page's last
  entry. radosgw's `RadosBucket::remove` lists the next page from the
  listing's next marker, the last entry of the page it has just removed
  ([rgw_sal_rados.cc:374](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L374),
  [rgw_sal_rados.cc:391](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L391);
  [rgw_sal_rados.cc:898-901](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L898-L901),
  [rgw_sal_rados.cc:918-921](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L918-L921)).
  When that entry is a delete marker, or a version whose instance entry is
  gone, the class finds no entry for the marker and starts past every
  version of its name
  ([cls_rgw.cc:375-381](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L375-L381),
  [cls_rgw.cc:425-431](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L425-L431)),
  so the key's versions further on that index shard are neither listed nor
  removed (docs/ceph-upstream-bugs.md, "radosgw's bucket removal with purge
  leaves a key's older versions behind when a page ends on one of its
  versions"). rgw-go sends the key's name with no instance as the next
  marker and asks the listing to start at that name's versions, as a
  continuation token without a version token starts
  (`ListObjectsParams.RelistMarker`, `internal/op/bucket.go`), so it lists
  and removes them, and its bucket removal finds the bucket empty where
  radosgw's leaves those versions behind it. Two pages in a row that
  remove nothing and start at the same entry are a key holding more
  versions the removal cannot remove than a page, and the next page starts
  past every version of that key, as radosgw's next marker passes it, so
  the keys after it are still removed. A page whose entries another
  removal took first goes on, as radosgw's goes on past any page. The
  listing ends at its end, and the bucket's removal then refuses what is
  left, where radosgw's `delete_bucket` removes the bucket over it ("A
  purging bucket removal checks the index is empty at the end"). A bucket
  that was never versioned is paged as radosgw pages it, since a marker
  without an instance starts after its name whether or not its entry is
  still there. The purges' refusal of a versioned bucket ("A purging bucket
  removal refuses a bucket whose versioning was ever enabled") comes before
  this listing. The lifecycle pass, which keeps most of what it lists,
  resumes after the newest version it kept instead ("A lifecycle pass hands
  each version of a key out once").
- **The admin bucket removal with bypass-gc leaves a versioned key's OLH
  head to the ordinary purge.** `DELETE /admin/bucket?bypass-gc=true`
  releases a version's tail stripes and removes its head as radosgw's
  `remove_bypass_gc` does, both gateways leaving the version's index entry
  marked gone under the removal's pending tag, and the ordinary purge that
  follows then removes the version by its instance, as radosgw's does. A key's OLH
  head object, which holds the key's OLH and the null version's data, is
  skipped instead: the ordinary purge removes the null version through the
  OLH and queues its tail stripes for the garbage collector. radosgw reads
  that head without following the OLH, releases the null version's tail
  stripes, and removes the head object whole, its OLH attributes with it,
  before its ordinary purge, which writes a delete marker for the null
  version rather than removing it (`docs/ceph-upstream-bugs.md`, "radosgw's
  bucket purge writes a delete marker in place of a versioned bucket's null
  version")
  ([rgw_sal_rados.cc:513-582](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L513-L582),
  [rgw_sal_rados.cc:534-603](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L534-L603);
  [rgw_rados.cc:10906-10908](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10906-L10908),
  [rgw_rados.cc:11647-11649](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11647-L11649)).
  So the null version's tail stripes wait for the garbage collector, where
  radosgw frees them at once. The purges' refusal of a versioned bucket
  ("A purging bucket removal refuses a bucket whose versioning was ever
  enabled") comes before this pass.
- **The admin account routes and the RADOS account store differ from
  radosgw in eight ways.** On `/admin/account` (`rgw_rest_account.cc` and
  `rgw_account.cc` at v19.2.6 and v20.2.4) and in the RADOS driver's
  account store (`driver/rados/account.cc` and `users.cc`):
  - A name or email redirect whose account is gone, or no longer holds
    that name or email, is stale. A lookup through it answers
    NoSuchEntity, where radosgw returns the account it names, under its
    new name for one a rename left behind: 404 NoSuchKey from the admin
    API, and from S3 a grant by that email answers 400
    UnresolvableGrantByEmailAddress in an ACL document or 404 NoSuchKey
    in a grant header, where radosgw grants the account
    (`internal/authz/write.go`). A create or rename takes a stale redirect over,
    where radosgw takes any redirect that reads for a holder and refuses
    the name or email with EEXIST for good (docs/ceph-upstream-bugs.md, "A
    stopped account removal or rename blocks the name and email for
    good"). An email redirect naming a user whose id has an account id's
    shape, which only a metadata put can store, reads as an account's,
    and is stale when no such account exists.
  - A create first writes its account object exclusively with no name
    or email, and writes the whole account last, under the version that
    write left, so a created account is at version 2 where radosgw's is
    at 1 (`rgw_account.cc:164`, one exclusive write under a new write
    version, at v19.2.6 and v20.2.4). A write claims the redirects of the name and email it
    holds, changed or not, before it writes the whole account, writing one
    that already names it again unchanged under the version read; radosgw
    writes them after, only for a changed name or email, and ignores
    their failure, so one failure there leaves two accounts holding one
    name (docs/ceph-upstream-bugs.md, "radosgw's account removal deletes a
    name redirect another account holds"). A failed claim leaves the
    account object without that name or email. A create whose claim fails
    gives up the name redirect it claimed, then removes its nameless
    object, each under the version it wrote, and keeps the object when the
    redirect's removal fails, so a redirect it claimed never names an
    account with no object. A claim another
    account holds, read past the metadata cache, answers 409,
    AccountAlreadyExists on create and BucketAlreadyExists on modify, even
    on a modify that changes neither, so an account whose name or email
    another account holds cannot be modified until it changes them; an
    account object stored without a version holds its redirects. A stale
    redirect naming an existing account is taken over only after that
    account's object is written again unchanged under the version read,
    and is then removed under the version read: a write of that account
    in flight fails 409 ConcurrentModification, and one that claims the
    redirect again meanwhile fails the removal, after which the takeover
    reads again. So rgw-go's own writes never leave two accounts holding
    one name or email. A create that stops, or whose whole-account write
    a takeover fenced, which answers 409 ConcurrentModification, leaves an
    account with no name or email. No lookup by name or email finds it; a
    read by id and radosgw's `metadata list account` do. It refuses a
    later create of the same caller-supplied id with 409
    AccountAlreadyExists, so retrying such a create never succeeds while
    it stands. One is left per lost race or stopped create, and the 409
    names no id, so one with a generated id is found only by listing. The
    operator removes it by id.
  - An account removal reads its name and email redirects past the
    metadata cache before it removes the account object, a failed read
    refusing it with nothing removed, and then removes each only when it
    named the account, under the version read then, so a create of the
    same id that claimed one meanwhile keeps it. radosgw removes
    both unread, freeing a name another account holds or an email a user
    took (docs/ceph-upstream-bugs.md, "radosgw's account removal deletes a
    name redirect another account holds" and "radosgw lets a user take an
    account's email and deletes it with the account"). The memstore keeps
    radosgw's removal.
  - A redirect step that fails is logged and the write goes on, as in
    radosgw, but the log names the account and the kind of redirect, not
    the object, which an email redirect is named by.
  - An account object stored without a version, which radosgw never
    writes, is written over only as a create, so a modify answers 409
    BucketAlreadyExists, and its removal answers 409
    ConcurrentModification; radosgw writes and removes it unchecked.
  - Create and modify answer 400 InvalidArgument after the cap check,
    storing nothing, for a `max-users`, `max-roles`, `max-groups`,
    `max-access-keys` or `max-buckets` that is empty, does not parse, or
    lies outside int32. radosgw stores 0 for an empty or unparsable one,
    which for `max-buckets` is no limit, and wraps one past int32, a
    negative limit being no limit for all but `max-buckets`
    (docs/ceph-upstream-bugs.md, "radosgw's account create and modify
    store an unparsable or wrapped limit, which can mean no limit").
  - On Tentacle, `PUT /admin/account?quota` answers 400 InvalidArgument
    after the cap check, storing nothing, for a `max-size` or
    `max-objects` that is empty, does not parse or lies outside int32, a
    `max-size` below -1, an `enabled` that does not parse, and any
    `max-size-kb`, which the op does not read. radosgw stores 0 for an
    empty or unparsable size or count, wraps one past int32, so that 3 GiB
    becomes no limit, disables the quota for an unparsable `enabled`, and
    ignores `max-size-kb` (docs/ceph-upstream-bugs.md, "radosgw's account
    quota set wraps a size past 2 GiB and defaults what it cannot parse").
    An empty `enabled=` keeps radosgw's reading as true.
  - An account removal on the RADOS driver refuses with radosgw's 409
    BucketNotEmpty and "The account cannot be deleted until all ... are
    removed." while an entry of the account's `roles.<id>`, `groups.<id>`
    or `topics.<id>` index remains, or the oidc pool holds an OpenID
    Connect provider of the account, as radosgw refuses for roles, groups
    and providers (`rgw_account.cc:366-432`). radosgw lists the topics of
    the account's tenant instead, so it removes an account that still
    owns topics, which rgw-go refuses (docs/ceph-upstream-bugs.md,
    "radosgw's account removal never sees the account's topics"). A
    failure to read an index or to list the pool refuses the removal. The
    memstore holds none of these resources.
- **The metadata sections write only what keeps a user or bucket whole.**
  The `user`, `bucket` and `bucket.instance` sections of the metadata store
  (`internal/driver/metadata.go` with a file per section beside it,
  `metadata_user.go`, `metadata_bucket.go` and `metadata_instance.go`;
  `internal/op/metadata.go`; and the memstore's) are radosgw's handlers
  (`driver/rados/rgw_user.cc` and `rgw_bucket.cc`, `rgw_metadata.cc` at
  v19.2.6 and v20.2.4) with these differences, each refusal coming before
  anything is written:
  - Every put reads its object by the key and writes under the version it
    read, or creates the object exclusively when it read none. radosgw's
    puts on Squid write under the version read too, but over an object
    they read none of they write neither guarded nor exclusively (`put_pre`,
    `rgw_metadata.cc:255-278` at v19.2.6; the user put's `store_user_info`
    with `exclusive` false, `driver/rados/rgw_user.cc:2831-2833` at
    v19.2.6), so two puts creating one entry both succeed and the last
    wins. On Tentacle radosgw writes a bucket instance unguarded
    (docs/ceph-upstream-bugs.md, "radosgw's bucket instance metadata put
    overwrites unguarded on Tentacle"). A put over an object that carries
    no version answers 500 InternalError. The version written is the
    document's when it has a tag, as radosgw's tracker writes it.
  - A key holding a NUL byte, on every get, put and remove, and a document
    naming one in a bucket id, marker, owner, explicit placement pool, attr
    name, user id, email, account id or access or Swift key id answer 400
    InvalidArgument, writing nothing: go-ceph cuts an object name at a NUL,
    so the name would reach another object (docs/cgo-limitations.md,
    "go-ceph cuts object names at a NUL byte").
    radosgw refuses a NUL in a request's decoded path alone, with 400
    InvalidRequest (`ERR_ZERO_IN_URL`, `rgw_rest.cc:2182-2186` at v19.2.6,
    `:2204-2208` at v20.2.4), not in the query the key arrives in or in a
    document, and stores the object under the whole name. It keeps a NUL
    in the key as in every other admin argument, decoding the key once
    where `get_string` decodes the others again (`frame_metadata_key`,
    `rgw_rest_metadata.cc:35-52` at v19.2.6 and v20.2.4), so the key gets
    the answer rgw-go gives a NUL in any admin argument that names a RADOS
    object ("A NUL in a request argument that names a RADOS object is
    refused"), from the sections, after authentication.
  - A document attr whose name lacks `user.rgw.` answers 400
    InvalidArgument on every put. radosgw sets each attr a document carries
    on the object in the op that sets the object's version, so one named
    `ceph.objclass.version` replaces that version and every later read of
    the object that reaches RADOS fails (docs/ceph-upstream-bugs.md,
    "radosgw's metadata put writes a document's attrs raw, the object's
    version among them"). A get shows the `user.rgw.` attrs alone, so no
    document read back carries another.
  - A document that does not decode answers 400 InvalidArgument, and one
    naming a sync policy with groups, which rgw-go carries opaque, 501
    NotImplemented. The decoders read Go's JSON grammar and refuse what
    radosgw's `decode_json` takes but cannot mean: a string member given
    null, an object or an array; a list or map given anything but an
    array, null included, or holding an entry that is no object; a time in
    a form other than the two radosgw's dump writes, `<sec>.<usec>` and the
    ISO date; a key without an id, an attr that is not padded base64, a
    hash type other than Mod, an index type or reshard status the enums
    lack, and a website redirect or error status code outside u16's range,
    which radosgw wraps into its u16. An entry point from before version 8,
    which embeds its bucket info, answers 400. The `otp` section's document
    is read by the same rules (`op.DecodeOTPDocument`,
    `internal/op/metadata_otp.go`): a device whose string member is given
    null, an object or an array, devices given anything but an array, null
    included, and a device that is no object answer 400 InvalidArgument,
    where radosgw's `decode_json` takes an object's members as the list and
    a device that is no object as one without members.
  - A user put whose document names another user than the key answers 400
    InvalidArgument; radosgw reads the key's user and writes the
    document's, keeping the key's indexes against the document's record.
    It also answers 400 InvalidSecretKey for a key without a secret, which
    anyone holding the key's id could sign with, 400 InvalidArgument for an
    account the store does not hold, an account of another tenant, a root
    user outside an account, and a user id or tenant in an account id's
    form, which a user create refuses and the email and key indexes would
    read as an account's, 409 EmailExists for an email another user or an
    account holds, whose index the put claims as a user write does ("The
    admin user routes answer store failures, and keep the account users
    index clean"), and 409 KeyExists for an access key the put
    gains whose id another user holds, active or not ("The admin key,
    subuser, caps and quota routes differ from radosgw in seven ways");
    radosgw's metadata put checks none of them but an active key's.
    It keeps the account users index as the user routes do, the entry
    added before the user and the old one removed after ("The admin user
    routes answer store failures, and keep the account users index"). The
    IAM group users index radosgw's write links is not written: rgw-go
    refuses a group member's requests outright.
  - A user removal refuses with 409 BucketAlreadyExists a user outside an
    account whose bucket list holds a bucket, as the user removal route
    does; radosgw removes the user and its bucket list, leaving its
    buckets in no owner's list.
  - A bucket entry point put answers 400 InvalidArgument for a document
    naming another bucket than the key, an instance that does not exist,
    or an owner other than that instance's, as such an entry point is a
    second owner to every later link, unlink and chown; 409
    BucketAlreadyExists for another bucket id over a stored entry point,
    which would leave that bucket without its name, and for a new entry
    point whose instance another name loads, which would give one bucket's
    index and data two names; and 409 ConcurrentModification while a live
    rename or a removal holds the bucket. radosgw writes the entry point
    the document gives. The owner's list follows the entry point written: a
    linked one is in its owner's list and an unlinked one is not, and a
    retry of a put that stopped after the write completes the list, where
    radosgw relinks only on a change from the old entry point, so a retry
    of its put leaves the lists as they were. An old owner's entry for the
    bucket goes too, but only on the put that moves the entry point to
    another owner: its retry reads the moved entry point as the old one, so
    a put that stops between leaves the old owner's list naming the bucket,
    which holds off that owner's removal until the entry is unlinked. The old entry point's attrs are
    kept on both releases; Tentacle's put writes the document's, which has
    none (`RGWBucketMetadataHandler::put`, `driver/rados/rgw_bucket.cc:2355-2401`
    at v20.2.4).
  - A bucket instance put takes the bucket's tenant, name and id from the
    key, where radosgw keeps a stored instance's name from the document.
    A stored instance keeps its marker, its index layout, its explicit
    placement and placement rule, as radosgw keeps the last two, and the
    attrs rgw-go keeps for itself, `user.rgw.rgw-go-rename` and
    `user.rgw.rgw-go-removing`, which a document cannot write; a document
    changing the marker, the shard count, the hash type or the index type,
    or turning object lock off, answers 400 InvalidArgument, where radosgw
    writes it, re-sharding the index under its entries. A put stores the
    object lock configuration the document carries as `obj_lock`, which
    is read while the document's flags enable object lock ("The bucket
    instance document carries `obj_lock` on Tentacle too"), as a v19.2.7
    put stores the instance it decodes whole
    ([rgw_common.cc:2588-2590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590),
    [rgw_bucket.cc:2866-2887](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L2866-L2887)).
    rgw-go stores it on Tentacle too, where radosgw's put never reads
    `obj_lock` and so drops the bucket's rule. For a document without
    `obj_lock`, a stored instance keeps its configuration and a new one
    gets the default, enabled with no rule, as on both floors. Keeping the
    stored one differs from both floors, which reset it to the default:
    v19.2.7 because a member the document lacks decodes default-constructed
    ([ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346)),
    and v20.2.4 on every put (docs/ceph-upstream-bugs.md, "radosgw's bucket
    instance metadata put drops the bucket's object lock rule"). The reset
    would drop a WORM bucket's default retention whenever a document lacks
    `obj_lock`, as one a v19.2.6 or v20.2.4 gateway dumped does. An owner
    other than that of the entry point
    naming the instance answers 400. A new instance's marker must be its
    id (400), and another instance carrying its id as its id or its marker
    answers 409 BucketAlreadyExists, as either would share another bucket's
    index or objects; the check lists the instances' keys, which carry
    their ids, and reads each instance for its marker, one read per
    instance. After the write and the index, the bucket's lifecycle
    entry is set when the document's attrs carry `user.rgw.lc` and removed
    when only the stored instance's did, as v20.2.4's put does
    ([rgw_bucket.cc:3010-3040](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L3010-L3040)),
    and a failure of either fails the put after its write, as on both
    releases, a shard whose lock stays held answering 503
    ServiceUnavailable. rgw-go keeps that rule on Squid, where v19.2.7's
    put also removes the entry when neither carries the attr
    ([rgw_bucket.cc:2900-2928](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L2900-L2928)):
    that removes only an entry no configuration the bucket held implied,
    and fails a put that leaves lifecycle alone whenever the shard's lock
    stays held. The instance's topic mappings, which radosgw updates after
    the write, are not kept by rgw-go yet.
  - The bucket puts serialize on a bucket claim, an object per bucket id
    in the domain root's pool under the domain root's namespace with
    `.rgw-go-bucket-claims` appended (`root.rgw-go-bucket-claims` in a
    default zone), which names the bucket the id belongs to and which
    radosgw neither reads nor writes (`internal/driver/bucketclaim.go`).
    An instance put creates its claim exclusively, for a new instance after
    the id and marker check, and each bucket put reads the claim before
    what it checks, marks it with the owner it writes, under the version it
    read, before its write, and clears the mark after; an entry point put
    finding no claim creates one after its checks and answers 409
    ConcurrentModification when another put created it meanwhile. So of
    two new instances naming one id under two names, one answers 409
    BucketAlreadyExists, and a claim naming another bucket answers that to
    every put of this one; of an entry point put and an instance put
    changing the owner that checked the same state, the second answers 409
    ConcurrentModification, as does a put meeting a mark that names another
    owner. A new instance's marker is its id, so the claim of the id
    serializes the marker too. A retry of a put that stopped after its mark
    goes through when it writes the marked owner, and a put of another
    owner answers 409 until a put of the marked owner completes. A put
    whose own write fails after its mark with a lost version race or
    exclusive create clears the mark best effort; any other failure leaves
    it, as a stop does, a context that ends among them, since the write
    may still land after it. A mark left with
    an entry point present names the bucket's owner then: when an admin
    link or chown, which take no claim, then moves the bucket to another
    owner, a put of the new owner answers 409 for the mark and a put of the
    marked owner 400 for the owner, until the operator chowns the bucket
    back, makes one put as the marked owner, which clears the mark, and
    chowns it forward again. A rename moves the claim to the new name
    before it writes anything there, keeping the old name in the claim,
    and drops the old name once the rename is done, unless a put has
    marked the claim then, so a put of either name finds the claim its own
    while the rename runs or after it stops, and its retry finds the claim
    moved. The move itself keeps a put's mark but changes the claim under
    that put, whose clear then fails and leaves the mark, another source of
    a left mark, recovered the same way. A link, unlink or chown changes
    neither the name nor the id, and leaves the claim. A claim is never
    removed, so an id stays its bucket's after the bucket's instance is
    gone. Errors and logs name a claim by its pool and kind,
    `bucket claim`. The claims fence rgw-go's metadata puts alone: a
    radosgw gateway, and rgw-go's own bucket create, link, chown, rename
    and removal, take no mark, so one of them racing a metadata put is fenced
    only by the version of the object both write, an instance one of them
    creates between a new instance's check and its claim is not seen, and
    an owner one of them changes after a put read the instance is not
    either. The checks that read beyond the claim, the instance listing for
    ids and markers and the entry point listing for a name loading the id,
    stay unfenced against those writers.
  - A bucket entry point is removed only once its instance is gone, a
    name that loads nothing, and an instance only while no entry point
    names it; either otherwise answers 409 ConcurrentModification, as
    removing it would leave a live bucket without its name or a name
    without its bucket. A bucket a removal or a live rename holds answers
    409 too. The entry point's removal unlinks the owner's list entry
    first and returns each failure, where radosgw logs them and answers
    200. On Tentacle an instance removal reads the instance and removes
    nothing, as radosgw's does (`driver/rados/rgw_bucket.cc:3062-3080` at
    v20.2.4).
  - A listing's marker is the token an rgw-go listing returned; a page
    holds the keys of up to its maximum objects the section keeps, and a
    maximum of 0 lists nothing and reports whether a key follows.
- **Admin listing markers are rgw-go's own.** A truncated `GET
  /admin/metadata/<section>` or `GET /admin/user?list` returns a marker
  that resumes the listing only on the gateway that issued it. radosgw's
  marker is a librados object cursor, and radosgw parses any marker it is
  given as one (`rgw_list_pool`, [rgw_tools.cc:354-358](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L354-L358);
  v20.2.4 [:370-374](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_tools.cc#L370-L374)).
  rgw-go's marker names the last key it returned and resumes after that
  key's placement hash, because the cursor go-ceph exposes marks the next
  batch rather than the last key, and rgw-go answers 400 InvalidArgument
  for a radosgw marker. go-ceph's rgw/admin package, Rook's client, sends
  no marker: its `GetUsers` asks for the unpaged list. A client that pages
  and moves between gateways mid-listing starts the listing again. A
  metadata marker of nothing but '=' lists from the beginning, as one that
  is not base64 does on both gateways, where radosgw reads before the
  marker's buffer
  ([radosgw's from_base64 reads before an all-'=' input](ceph-upstream-bugs.md#radosgws-from_base64-reads-before-an-all--input)).
- **The admin metadata API serves four sections, and differs from
  radosgw's in five more ways.** `GET`, `PUT` and `DELETE
  /admin/metadata` are radosgw's `RGWHandler_Metadata` ops
  (`rgw_rest_metadata.cc`, the same at v19.2.6 and v20.2.4) over the
  sections "The metadata sections write only what keeps a user or bucket
  whole" describes and the `otp` section ("An otp put's header lists
  exactly the document's devices"), with these differences:
  - Only the `user`, `bucket`, `bucket.instance` and `otp` sections are
    served, and the top-level listing, `GET /admin/metadata`, lists those
    four. radosgw also serves and lists `account`, `group`, `roles` and
    `topic`, for each of which rgw-go answers 404 NoSuchKey.
  - A put's `update-type` decides whether it applies on Tentacle too, and
    in the `otp` section on both releases: `update-by-version` applies a
    document only over the same tag at a lower version and
    `update-by-timestamp` only over an older mtime, as Squid's
    `check_versions` decides, and a put they refuse answers 204 with
    `RGWX_UPDATE_STATUS: skipped`. Tentacle's radosgw checks the argument
    and then applies every put
    ([Tentacle's metadata put ignores update-type](ceph-upstream-bugs.md#tentacles-metadata-put-ignores-update-type)),
    and both releases' otp handlers apply every otp put, as neither reads
    the entry before it writes
    ([radosgw's otp metadata put ignores update-type](ceph-upstream-bugs.md#radosgws-otp-metadata-put-ignores-update-type)).
    An otp put the check lets through writes unguarded, as radosgw's does,
    so a write landing between the check's read and the put is overwritten.
  - A put's `RGWX_UPDATE_VERSION` names the version the put read, in every
    section but `otp`, whose put reads none and names `ver:0,tag:` on both
    releases, as radosgw's does. radosgw's user section names the version
    read too, but its `bucket` and `bucket.instance` puts name the version
    their write left, as the write updates the tracker the header is read
    from (`RGWObjVersionTracker::apply_write`,
    `driver/rados/rgw_rados.cc:185-197` at v19.2.6, `:220-232` at v20.2.4).
  - A put's document is read up to 16 MiB, and a longer one answers 400
    InvalidArgument; radosgw reads a body of any length into memory
    (`RGWOp_Metadata_Put::get_data`, `rgw_rest_metadata.cc:176-219`). A
    body that ends short of its Content-Length answers 400 RequestTimeout.
  - The document's `ver` must be an object whose `tag` is a string and
    whose `ver` is a number, and its `mtime` must be in one of the two
    forms a get writes; anything else answers 400 InvalidArgument, as the
    sections' decoders refuse what `decode_json` takes but cannot mean.
    radosgw takes a `ver` that is no object as the zero version, a tag
    given as an object as that object's text, and an mtime in any form
    `utime_t::parse_date` reads.
  - An `otp` put answers 400 InvalidArgument, writing nothing, for a
    document holding a device without an id, or whose seed the class would
    parse into an empty key: an empty seed, a `hex` seed led by a NUL and a
    `base32` seed of nothing but spaces. It does the same for a device of a
    type other than TOTP, 2, of a seed type other than `hex` or `base32`,
    of a `step_size` of 0 or over 3600 seconds, of a `window` over 1024
    steps, or with a `seed` longer than 1024 bytes, counted as the bytes of
    the decoded JSON string, every device checked before the write
    (`op.DecodeOTPDocument`, `internal/op/metadata_otp.go`). radosgw writes
    each. A key holding a NUL byte is refused as in every section. Any
    `time_ofs` is written, as radosgw writes it: the class only subtracts
    it from the clock before it divides, so no cost grows with it.
    - `radosgw-admin mfa create` refuses a device whose id or seed is
      empty, and a seed type other than `hex` or `base32`
      ([rgw_admin.cc:10799-10817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10799-L10817),
      [radosgw-admin.cc:11390-11408](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L11390-L11408)),
      and creates only TOTP devices, `otp_info_t`'s default
      ([cls_otp_types.h:32](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L32),
      [cls_otp_types.h:32](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_types.h#L32)).
      The class hands liboath the seed's `c_str()`, whose hex decode stops
      at the first NUL and whose base32 decode drops every space, and both
      answer an empty key, whose codes anyone can make, for the three seeds
      above; `mfa create` takes the base32 one
      ([radosgw's otp metadata put stores a device without a seed, whose codes anyone can make](ceph-upstream-bugs.md#radosgws-otp-metadata-put-stores-a-device-without-a-seed-whose-codes-anyone-can-make)).
      The class also checks every device as TOTP, and parses a seed of any
      type but base32 as hex.
    - The class divides by a step of 0
      ([cls_otp divides by a stored step_size that is never validated](ceph-upstream-bugs.md#cls_otp-divides-by-a-stored-step_size-that-is-never-validated))
      and declares a stack array as long as each seed it parses
      ([cls_otp's otp_set puts an array as long as the seed on the OSD's stack](ceph-upstream-bugs.md#cls_otps-otp_set-puts-an-array-as-long-as-the-seed-on-the-osds-stack)).
      1024 bytes is 8 times the 128 hex digits of RFC 6238's longest key,
      SHA-512's 64 bytes.
    - Every check of a device runs liboath's walk over its window in the
      class, up to two HOTP codes for each step of it, so a wrong code
      costs the OSD 2·window+1 codes, over eight billion at the largest
      window radosgw writes
      ([cls_otp verifies any stored window, an OSD CPU cost per check](ceph-upstream-bugs.md#cls_otp-verifies-any-stored-window-an-osd-cpu-cost-per-check)).
      1024 is 512 times `otp_info_t`'s default window of 2, which
      `radosgw-admin mfa create` keeps unless given another
      ([cls_otp_types.h:40-41](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp_types.h#L40-L41),
      [cls_otp_types.h:40-41](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp_types.h#L40-L41)),
      over eight hours each way at its 30 second step.
    - The class keeps a device's checks for one step back from the OSD's
      clock, in unsigned nanoseconds, so a step longer than the clock's
      seconds wraps and trims every check, taking off the device's five
      checks per step, and its lookup never finds a check of the device
      again, so no code of it verifies
      ([cls_otp never verifies a device whose step is longer than the clock's seconds](ceph-upstream-bugs.md#cls_otp-never-verifies-a-device-whose-step-is-longer-than-the-clocks-seconds)).
      3600 seconds is 120 times RFC 6238's 30 second step, `otp_info_t`'s
      default.
    - Only writes are held to these rules: a device radosgw wrote with a
      longer seed, a wider window, a longer step, a step of 0 or any of the
      others still reads back. An MFA check of one with an empty key, such
      a step or such a window sends nothing and does not verify ("A stored
      MFA device outside the otp put's bounds does not verify").
- **An otp put's header lists exactly the document's devices.** The `otp`
  section's put (`internal/driver/metadata_otp.go`) is radosgw's
  `RGWSI_Cls::MFA::set_mfa` with `reset_obj`, one write op of a remove
  allowed to fail, a non-exclusive create, `prepare_mfa_write`'s version
  and mtime, and `otp_set` with the document's devices
  ([svc_cls.cc:65-87](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L65-L87),
  [svc_cls.cc:188-216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_cls.cc#L188-L216),
  the same at v20.2.4), then a raw omap write of the object's `header`,
  `otp_header` v1 in the class's bytes, holding exactly the document's ids.
  The OSD serves `otp_set`'s reads from the stored object, so radosgw's put
  writes a header listing the stored ids beside the document's, and keeps
  the id of each device the document drops, whose record the remove
  dropped. Every later `get_all` of the object then fails, its metadata get
  answers 404 NoSuchKey, and metadata sync removes the object on the peer
  zones
  ([radosgw's otp metadata put keeps a dropped device's id in the otp header](ceph-upstream-bugs.md#radosgws-otp-metadata-put-keeps-a-dropped-devices-id-in-the-otp-header)).
  rgw-go's header keeps no such id, so its put of an object radosgw left
  so mends it. On both gateways a device the document keeps keeps its
  replay state, `last_checks` and `last_success`.
- **The bucket instance document carries `obj_lock` on Tentacle too.** A
  `bucket.instance` metadata get of a bucket whose flags enable object lock
  writes the bucket's object lock configuration as `obj_lock`, after
  `new_bucket_instance_id`, and a put's document is read for one while its
  own flags enable object lock, as v19.2.7 writes and reads it
  ([rgw_common.cc:2538-2544](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2538-L2544),
  [rgw_common.cc:2588-2590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590)), on
  both releases: `enabled` only when true, then `rule_exist`, then, when the
  rule exists, `rule` holding `defaultRetention` with `mode` and `days` when
  positive, else `years`
  ([rgw_object_lock.cc:93-101](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L93-L101),
  [rgw_object_lock.cc:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L56-L58),
  [rgw_object_lock.cc:26-33](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L26-L33)). A
  member `obj_lock` lacks reads as its zero value
  ([rgw_object_lock.cc:65-71](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L65-L71),
  [ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346)), so `{}` is disabled with no
  rule, and a document without `obj_lock` gives the put no configuration.
  v20.2.4's dump writes no `obj_lock` and its decode reads none
  ([rgw_common.cc:2577-2604](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2577-L2604),
  [rgw_common.cc:2606-2647](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2606-L2647)), so
  a Tentacle radosgw ignores the key in a document rgw-go wrote. Its
  `RGWObjectLock::dump`, which no metadata document reaches, keeps an older
  form ([rgw_object_lock.cc:77-85](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L77-L85)). A
  stored configuration that does not decode fails the get. What a put
  stores is the bucket instance put's, "The metadata sections write only
  what keeps a user or bucket whole" (docs/ceph-upstream-bugs.md, "radosgw's
  bucket instance metadata put drops the bucket's object lock rule").
- **`GET /admin/user?list` reads an unparsable max-entries as 1000.** rgw-go
  pages by the default when `max-entries` is not a number `stringtoul`
  reads, where radosgw pages by an uninitialized variable, capped at 1000
  ([radosgw's user listing takes an unparsable max-entries from an uninitialized variable](ceph-upstream-bugs.md#radosgws-user-listing-takes-an-unparsable-max-entries-from-an-uninitialized-variable)).
- **The admin index check does not re-read every object's head.** `GET
  /admin/bucket?index&check-objects=true&fix=true` lists the bucket's
  objects and reports their names, as radosgw does, but radosgw's listing
  also reads the head of every object it lists and repairs the index from
  it: an entry whose object is gone is dropped from the report and removed
  from the index, and the others are refreshed
  (`rgw_bucket_object_check_filter`,
  [rgw_bucket.cc:141-146](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L141-L146),
  forcing `check_disk_state` in the listing,
  [rgw_rados.cc:9820-9864](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9820-L9864)).
  rgw-go's listing repairs only entries with a pending operation, as every
  listing does, so it reports and keeps such an entry. `radosgw-admin
  bucket check --check-objects --fix` repairs it. radosgw also sets every
  shard's tag timeout to 30 seconds, `BUCKET_TAG_QUICK_TIMEOUT`, for that
  listing and to 0 after it (`set_tag_timeout`, rgw_bucket.cc:437 and :465
  at v19.2.6, :574 and :602 at v20.2.4), so its forced check expires
  pending operations sooner; rgw-go writes no tag timeout.
- **The admin index check's fix keeps the entries of an object with an
  upload in flight.** `GET /admin/bucket?index&fix=true` reports under
  `invalid_multipart_entries` what radosgw reports, by each release's
  rule, but removes a reported entry only when no upload of its object is
  in flight: after its check, rgw-go lists the bucket's `multipart`
  namespace again and keeps every reported entry whose object, the
  entry's name with its last two `.`-components removed, has a `.meta`
  entry there. radosgw removes every entry it reports, and it reports the
  head of a part sent twice, whose name carries a random prefix where the
  upload id goes, and on Tentacle also a part that another upload's
  entries separate from its `.meta` entry on the shard; its fix then
  drops a live upload's part from the bucket's listing and stats until the
  upload ends ([radosgw-admin bucket check reports a live re-uploaded part
  as an invalid multipart
  entry](ceph-upstream-bugs.md#radosgw-admin-bucket-check-reports-a-live-re-uploaded-part-as-an-invalid-multipart-entry),
  [Tentacle's bucket check reports a live part that another upload's
  entries separate from its meta
  entry](ceph-upstream-bugs.md#tentacles-bucket-check-reports-a-live-part-that-another-uploads-entries-separate-from-its-meta-entry)).
  rgw-go also keeps an orphan of an object that has another upload in
  flight, which a fix after that upload ends removes, and
  `radosgw-admin bucket check --fix` removes at once. Both releases' checks
  read the `multipart` namespace through rgw-go's bucket listing, which
  removes a part entry with a pending operation whose head is gone, and
  leaves it out of the report, with or without `fix`. Squid's check lists
  that way too, but Tentacle's reads the raw entries (`bi_list`,
  rgw_bucket.cc:381-382 at v20.2.4) and reports such a part, so on
  Tentacle rgw-go leaves out a part radosgw reports, and removes its entry
  even without `fix`.
- **The admin usage trim is bounded.** `DELETE /admin/usage` repeats
  `user_usage_log_trim` on each usage shard object until the class answers
  ENODATA, as radosgw does, but after each round that answers 0 it reads
  the shard again with the same filter, and once a round removed nothing
  that read sees it stops and answers 200, logging a warning that names
  the two defects below. radosgw repeats the call with no bound
  (`cls_rgw_usage_log_trim_repeat`, [rgw_rados.cc:10197-10211](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10197-L10211);
  v20.2.4 [:11121-11135](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11121-L11135)).
  Each call walks its window from the range's start, so a round removes
  only the matching records the window reaches, and once a round removes
  nothing every later one does the same; on Squid and Tentacle ENODATA
  then never comes while a record whose payer is not its owner is in
  range, because the class stores it under the payer and deletes under
  the owner (tracker #72593, fixed in v21 only; [cls_rgw usage trim never removes a payer-keyed record](ceph-upstream-bugs.md#cls_rgw-usage-trim-never-removes-a-payer-keyed-record)),
  or while 1000 records of other buckets fill the front of a
  bucket-filtered trim's window (#58136; [cls_rgw usage trim with a bucket filter stalls behind 1000 other records](ceph-upstream-bugs.md#cls_rgw-usage-trim-with-a-bucket-filter-stalls-behind-1000-other-records)).
  There radosgw's request spins without answering, and rgw-go's answers
  200 having removed every record radosgw's rounds remove, the rest left in
  place. As a backstop rgw-go also stops a shard, with a warning of its
  own, after 10 times ceil(n/1000)+1 rounds that each removed something, n
  being what the first read of the shard counts, 1000 for each full page of
  keys and the records of its last; there it can leave records radosgw
  would go on to remove.
- **The admin usage routes give a date they cannot parse a value of their
  own.** radosgw reads `GET` and `DELETE /admin/usage`'s `start` and `end`
  with `RESTArgs::get_epoch` and drops its return, and
  `utime_t::parse_date` writes no epoch for a date it cannot parse, so
  both ops go on with what their uninitialized variables held
  ([radosgw's usage show and trim take an unparsable date from an uninitialized variable](ceph-upstream-bugs.md#radosgws-usage-show-and-trim-take-an-unparsable-date-from-an-uninitialized-variable)).
  rgw-go's `GET` reads such a date as the argument's default, 0 for
  `start` and 2^64-1 for `end`. Its `DELETE` answers 400 InvalidArgument
  after the cap check and trims nothing, since a default `start` would
  trim more than the range asked for. A date with a `%` conversion rgw-go
  does not run is such a date to both ("A date whose time names a
  conversion rgw-go does not run is refused").
- **A usage show of a bucket an account owns answers 500.** `GET
  /admin/usage` naming a bucket an account owns answers 500 UnknownError
  with its error document, as radosgw maps `RadosBucket::read_usage`'s
  `-ENOTSUP` ([rgw_sal_rados.cc:785-797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L785-L797);
  v20.2.4 [:803-815](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L803-L815)).
  radosgw meets that failure after its flusher has started the body
  (`RGWUsage::show`, `rgw_usage.cc:48-69` at both tags), so it answers 200
  with the body cut short inside the usage section ("Admin responses carry
  a Content-Type on every body"). The trim of such a bucket answers 500
  UnknownError from both.
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
  1024 bytes, which holds a signed SHA512 trailer with room to spare, keeps
  the trailers of all of it, and answers 409 LimitExceeded above that,
  keeping none. A signed trailer that radosgw refuses with 403 therefore
  succeeds through rgw-go.
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
    compare. rgw-go keeps a trailing checksum only from the trailer section
    after the final chunk, which it never reads past a length the chunks
    overrun, so it keeps none from such a payload and accepts it where
    Tentacle answers BadDigest for a checksum.
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
- **aws-chunked trailer sections are read line by line.** For every
  aws-chunked payload whose `x-amz-content-sha256` ends in `TRAILER`,
  rgw-go reads the trailer section as lines that end in CRLF, in any order,
  each split at its first colon into a name and an untrimmed value. For
  each name `x-amz-trailer` lists, the first line bearing exactly that name
  is that trailer, and rgw-go keeps its value, which a lookup matches in
  any ASCII case. Only `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER` has a
  trailer signature, and for it the signature rgw-go computes covers each
  trailer it keeps. Any other such payload's trailers are kept unsigned, as
  radosgw takes them. The declared signature is the value of the first
  line named `x-amz-trailer-signature`, never a trailer; a declared
  signature that is missing or differs is 403 SignatureDoesNotMatch, and no
  trailer is kept.
  radosgw instead searches the up to 255 bytes it reads after the chunk at
  the decoded length for `x-amz-trailer-signature:` and for each listed
  name anywhere, takes the text from the first match of each to the next
  CRLF as its line, splits that line at every colon, and puts each trailer
  line it finds into its trailer signature and into the request's
  environment, where a Tentacle radosgw's checksum check reads it
  (`extract_helper`, `split_header`, `extract_trailing_headers` and
  `complete()`, `rgw_auth_s3.cc:1436-1510` and `:1583-1690` at v19.2.6,
  `:1413-1487` and `:1560-1667` at v20.2.4). A section as the AWS SDKs
  write it, one line per listed trailer and the signature line last, with
  `x-amz-trailer` naming each trailer once, gets the same answer and the
  same trailers from both when it fits radosgw's buffer. They differ as
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
    signature covers, and its environment takes as a trailer, the text
    from that occurrence to the line's CRLF, split at its colons, even when
    the name also has a line of its own further on. rgw-go's signature
    covers, and rgw-go keeps, the first line that bears exactly the name,
    or no line for it.
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
    trailer signature and its environment; rgw-go takes the whole value
    after the first colon, an empty one included, both for the declared
    signature and for the trailers it keeps and its signature covers
    (`docs/ceph-upstream-bugs.md`, "radosgw reads an aws-chunked trailer
    line's value only up to a second colon, and drops an empty one").
  - A payload whose `x-amz-content-sha256` does not end in `TRAILER`, such
    as `STREAMING-AWS4-HMAC-SHA256-PAYLOAD`, sent with `x-amz-trailer`:
    radosgw takes the listed trailers from its section all the same, since
    `complete()` calls `extract_trailing_headers` whenever `x_amz_trailer`
    is set
    ([rgw_auth_s3.cc:1681-1683](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L1681-L1683)
    at v19.2.7,
    [rgw_auth_s3.cc:1636-1638](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1636-L1638)
    at v20.2.4). A Tentacle radosgw then verifies the checksum an algorithm
    header, or a part's upload type, names against such a trailer, which
    neither the payload's form nor any signature covers. It does so in the
    filter `RGWPutObj_Cksum::Factory` builds
    ([rgw_op.cc:4601-4611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611)),
    which a part with no algorithm header bases on the upload's
    `multipart_cksum_type`
    ([rgw_op.cc:4471-4472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472)).
    rgw-go reads no trailer from such a payload.
- **Some bucket sub-records are carried opaque in phase 0.**
  RGWBucketInfo's object-lock configuration and sync policy are kept as
  the encoded bytes radosgw wrote and written back unchanged; the compat
  check of each is still applied. This keeps them byte-exact by
  construction. The object-lock configuration is decoded where it is
  read, and PutObjectLockConfiguration encodes the one it stores. A
  configuration that passes the compat check but does not decode, a
  configuration radosgw never writes, loads with its bucket: rgw-go's
  GetObjectLockConfiguration answers it 500 UnknownError, and its
  PutObjectLockConfiguration replaces it, where radosgw fails every load
  of the bucket with -EIO before either op runs
  ([svc_bucket_sobj.cc:363-369](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_bucket_sobj.cc#L363-L369),
  [svc_bucket_sobj.cc:312-318](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bucket_sobj.cc#L312-L318)).
  The phase that serves bucket sync policy models it. The admin API's `GET` or `PUT
  /admin/metadata/bucket.instance` of a bucket holding a sync policy with
  groups answers 501 NotImplemented, where radosgw renders the record, and
  so does a put of a document that holds none: the put reads the stored
  record in its rendered form before it writes.
- **Zonegroup and period JSON with sync policy groups is not rendered.** The
  zonegroup's and period's sync policy is also kept opaque, and rgw-go
  refuses to render their JSON when a sync policy holds groups. Rook creates
  no sync policies on a single-site store, and multisite is excluded, so a
  Rook-managed cluster is not affected; an operator who ran
  `radosgw-admin sync group create` would be. The admin API's
  `GET /admin/realm/period` answers 501 NotImplemented for such a period,
  where radosgw renders it.
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
  - Beyond those three, the layout document `?layout` sends walks the
    manifest from `obj_begin` to `obj_end` as `dump_obj_layout` does
    ([rgw_sal_rados.cc:2807-2823](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2807-L2823),
    [rgw_sal_rados.cc:3642-3658](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3642-L3658)), under `Stripes`'
    bound. It fails with UnknownError at the first step that does not move
    past the previous offset and past `meta.MaxStripes` stripes, and refuses
    at once a manifest whose tail needs more stripes than that in the
    largest stripes the rules allow (`driver.Store.ObjectLayout`). So it
    refuses an object over about 8 TiB in the default 4 MiB stripes, which
    radosgw lays out. radosgw's loop has neither check: on a walk that stops
    moving it never ends, and the document it builds grows without bound.
    - The driver renders the document twice: once to measure it, which is
      where it fails, before any of it is sent, and once as it writes it.
      So a request holds the head, the manifest and one stripe's entry at a
      time. An entry is about 200 bytes for a short key, and up to about
      6.6 KB for a 1024-byte key whose every byte `json_stream_escaper`
      writes as `\u00xx`, so a document at the bound runs from about
      400 MB to about 13 GB.
    - radosgw holds the document whole: in its JSONFormatter until
      `send_response` flushes it once
      ([rgw_rest_s3.cc:4381-4384](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4381-L4384),
      [rgw_rest_s3.cc:4892-4895](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4892-L4895)), then in
      the buffering filter that measures its Content-Length
      ([rgw_client_io_filters.h:179-237](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_client_io_filters.h#L179-L237),
      [rgw_client_io_filters.h:179-237](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_client_io_filters.h#L179-L237)).
- **`?layout` of an object without a manifest fails.** radosgw's
  `dump_obj_layout` dereferences the null manifest `get_state` hands out for
  a head without `user.rgw.manifest`
  ([rgw_sal_rados.cc:2806](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2806),
  [rgw_sal_rados.cc:3641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3641)), which would end the radosgw
  process (`docs/ceph-upstream-bugs.md`, "[radosgw's ?layout dereferences
  a null manifest](ceph-upstream-bugs.md#radosgws-layout-dereferences-a-null-manifest)").
  rgw-go's layout fails with InternalError instead
  (`driver.Store.ObjectLayout`), which `?layout` answers as a 500. Neither
  gateway writes such a head under a key an S3 request names, so only a
  head another writer stored, or one whose manifest attr was removed, meets
  the difference.
- **`?layout` masks a cloud tier's S3 secret.** radosgw's `?layout` dumps a
  cloud-transitioned head's manifest, whose tier config holds the tier's S3
  key, and `RGWZoneGroupPlacementTierS3::dump` writes the secret in the clear
  ([rgw_zone.cc:966-979](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_zone.cc#L966-L979),
  [rgw_zone.cc:1006-1019](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone.cc#L1006-L1019)), to any holder of
  `admin=read` (`docs/ceph-upstream-bugs.md`, "[radosgw's ?layout hands a
  cloud tier's S3 secret to any holder of
  admin=read](ceph-upstream-bugs.md#radosgws-layout-hands-a-cloud-tiers-s3-secret-to-any-holder-of-adminread)").
  rgw-go writes `"secret":"******"` for a set secret and `""` for an empty
  one, the form `dump_mask_keys` in
  [ceph/ceph#68690](https://github.com/ceph/ceph/pull/68690) used, and
  keeps the access key and the document's shape
  (`driver.Store.ObjectLayout`, through `meta.MaskTierSecrets`). Compatibility does not mean reproducing
  radosgw's defects (spec §8): the stored manifest is unchanged, and only
  the rendered document differs.
  - No s3-tests test reads `?layout`, so the parity check's known-differences
    files carry no line for it. WP-24's golden captures of `?layout` bodies
    must carry the masked bytes for a tiered object.
  - The head reaches `?layout` only where lifecycle keeps it, which needs
    the tier's non-default `retain_head_object`, and never for a
    versioning-enabled bucket's current version; a versioning-suspended
    bucket's current version keeps it, as the registry entry states.
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
  (`driver/rados/rgw_obj_manifest.h:303-318` at both tags), differs:
  CompleteMultipartUpload answers 400 InvalidPart for an upload holding
  such a part, which radosgw completes, and leaves the upload in place.
- **Client checksums are ignored until phase 2.** rgw-go's op layer reads
  the checksum requests of PutObject, UploadPart, CreateMultipartUpload and
  CompleteMultipartUpload as the "Object write differences" entries from "A
  checksum algorithm rgw-go does not compute is refused at write" to
  "CompleteMultipartUpload reads one checksum header, of any type"
  describe, and the RADOS driver computes, verifies, stores and combines
  what the ops ask for, but the S3 handler does not yet hand the ops the
  checksum headers. So in the binary rgw-go ignores the `x-amz-checksum-*`
  headers and aws-chunked trailers of PutObject, UploadPart and
  CompleteMultipartUpload, and `x-amz-checksum-algorithm` on
  CreateMultipartUpload, on both releases. A Squid radosgw ignores them
  too, so on Squid nothing differs. A Tentacle radosgw validates a supplied
  checksum, answering 400 BadDigest on a mismatch, and stores the object's
  checksum in `user.rgw.cksum` (`rgw_op.cc:4757-4789` for PutObject and
  UploadPart, `:7295-7341` for CompleteMultipartUpload, at v20.2.4); it
  also keeps the algorithm CreateMultipartUpload names in the upload's
  `multipart_upload_info` (`:7006-7010`). On a Tentacle cluster a PutObject
  through rgw-go, and an upload rgw-go began, therefore carry no
  `user.rgw.cksum`: GetObjectAttributes and a checksum-mode GET through
  radosgw show no checksum for them, and rgw-go accepts a checksum that
  does not match the data. A CopyObject keeps or drops its source's record
  as radosgw's does, but for a composite one, which a copy that streams
  drops ("A copy that streams its data drops a composite checksum"). An
  upload a Tentacle radosgw began with a
  checksum algorithm keeps it through rgw-go: rgw-go reads the upload's
  type, computes it over each part it writes, a copied one included, with
  the upload's flags, and combines the parts' checksums into the object's
  at completion, so either gateway completes the upload ("Multipart
  uploads in flight are shared"), though rgw-go verifies no value a part or
  the completion supplies; an upload of a type rgw-go does not compute is
  answered 501 ("A checksum algorithm rgw-go does not compute is refused at
  write"). rgw-go also accepts what a Tentacle radosgw refuses: an
  `x-amz-checksum-algorithm` or `x-amz-sdk-checksum-algorithm` that names
  no algorithm it knows, which fails PutObject and UploadPart with EINVAL,
  400 InvalidArgument (`RGWPutObj_Cksum::Factory`,
  `rgw_cksum_pipe.cc:40-62`, caught at `rgw_op.cc:4602-4611`), and an
  `x-amz-checksum-type` that does not suit CreateMultipartUpload's
  algorithm, which fails it with 400 InvalidRequest
  (`rgw_rest_s3.cc:4508-4520`, `permitted_cksum_algo_and_type` in
  `rgw_cksum.h`). CreateMultipartUpload takes an algorithm Tentacle does
  not know as none, as rgw-go does. Phase 2 adds Tentacle-level
  checksums.
- **ListParts lists the parts of an upload whose checksum does not decode,
  and shows no part's checksum; a retried CompleteMultipartUpload shows no
  checksum.** Tentacle's ListParts decodes the upload's `user.rgw.cksum`
  and answers 500 UnknownError, -EIO, when it does not decode
  (`rgw_op.cc:7617-7630` at v20.2.4); rgw-go reads no checksum there and
  lists the parts. Tentacle's ListParts also renders each part's checksum
  of an AWS type from its part record as a `<Checksum*>` element
  ([rgw_rest_s3.cc:4709-4713](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4709-L4713)),
  which rgw-go does not. A retried CompleteMultipartUpload that a Tentacle
  radosgw answers 200 from an object it completed with a checksum
  (`user.rgw.cksum`) carries the `<Checksum*>` and `<ChecksumType>`
  elements in its response (`rgw_rest_s3.cc:4635-4639`, the checksum
  decoded at `rgw_op.cc:7474-7501` in `check_previously_completed`, where a
  decode failure is logged and still answers 200, `:7498-7500`, all at
  v20.2.4); rgw-go answers the same 200 without them.
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
- **A copy that streams a compressed source copies its stored bytes.**
  Where a copy cannot share the source's tails (another pool, placement or
  storage class, or a source held in its head), rgw-go writes a compressed
  source's stored bytes unchanged, keeping its compression attribute, block
  map included, and its accounted size, whatever the destination
  placement's compression type, as a Squid radosgw copies every source
  (`copy_obj_data`,
  [rgw_rados.cc:5066-5153](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5066-L5153)).
  A Tentacle radosgw decodes such a source instead and encodes the copy
  with the destination placement's compression type, or stores it decoded
  where that type is none (`RGWCopyObjDPF::set_writer`,
  [rgw_op.cc:5770-5844](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5770-L5844)),
  so the two gateways' copies can differ in codec and block layout; each
  reads back identically through either gateway. An uncompressed source
  that a copy streams into a placement with a compression type is
  compressed as a PUT is, on both releases, as a Tentacle radosgw
  compresses it; a Squid radosgw, whose copy has no compression stage,
  stores it uncompressed. rgw-go refuses an encrypted copy source with 501
  NotImplemented on both releases, as a Squid radosgw does
  ([rgw_rados.cc:4787-4795](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4787-L4795));
  a Tentacle radosgw decrypts it and re-encrypts as the request asks
  ([rgw_op.cc:5791-5804](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5791-L5804),
  [rgw_op.cc:5846-5865](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5846-L5865)),
  which needs SSE-C decryption, which phase 4 adds, or a key server, which
  is excluded.
- **CreateBucket with object lock is authorized as
  s3:PutBucketObjectLockConfiguration and s3:PutBucketVersioning too.**
  radosgw's CreateBucket authorizes s3:CreateBucket alone on the new bucket,
  whatever `x-amz-bucket-object-lock-enabled` asks
  ([rgw_op.cc:3215-3244](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3215-L3244),
  [rgw_op.cc:3443-3472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3443-L3472);
  `docs/ceph-upstream-bugs.md`, "[radosgw's CreateBucket with object lock
  skips the two permissions Amazon S3 requires for
  it](ceph-upstream-bugs.md#radosgws-createbucket-with-object-lock-skips-the-two-permissions-amazon-s3-requires-for-it)").
  Amazon S3's CreateBucket reference, retrieved 2026-10-21, requires both:
  "In your `CreateBucket` request, if you set
  `x-amz-bucket-object-lock-enabled` to true, the
  `s3:PutBucketObjectLockConfiguration` and `s3:PutBucketVersioning`
  permissions are required." Once the header reads true, rgw-go evaluates
  each against the requester's own policies on the new bucket's ARN, as it
  evaluates s3:CreateBucket, since no bucket policy exists yet, and answers
  a denial of either with 403 AccessDenied, creating nothing. A user outside
  an account whose policies decide neither is allowed both, as it is
  s3:CreateBucket; an account's user needs a policy that allows each. An
  admin is let through a denial, as through the permission check. So where
  radosgw creates a bucket with object lock for a requester a policy denies
  either action, rgw-go refuses it. rgw-go makes the check before it finds
  that the bucket exists, as Amazon S3 does, since the rule follows the
  headers the request sends: a denied requester re-creating a bucket it
  already owns, with the header true, gets 403 and the bucket is left as it
  was. radosgw answers such a re-create, when its placement and ACL match
  the bucket's, 200, taking the -ERR_BUCKET_EXISTS of its owner's existing
  bucket
  ([rgw_sal_rados.cc:173-208](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L173-L208),
  [rgw_sal_rados.cc:183-225](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L183-L225))
  for success
  ([rgw_rest_s3.cc:2554-2555](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2554-L2555),
  [rgw_rest_s3.cc:2707-2708](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2707-L2708)).
- **CreateBucket with an ACL other than private is authorized as
  s3:PutBucketAcl too.** radosgw's CreateBucket authorizes s3:CreateBucket
  alone, whatever ACL the request's headers build
  ([rgw_op.cc:3215-3244](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3215-L3244),
  [rgw_op.cc:3443-3472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3443-L3472);
  `docs/ceph-upstream-bugs.md`, "[radosgw's CreateBucket takes an ACL other
  than private without
  s3:PutBucketAcl](ceph-upstream-bugs.md#radosgws-createbucket-takes-an-acl-other-than-private-without-s3putbucketacl)").
  Amazon S3's CreateBucket reference, retrieved 2026-10-21: "In your
  `CreateBucket` request, if you specify an access control list (ACL) and
  set it to `public-read`, `public-read-write`, `authenticated-read`, or if
  you explicitly specify any other custom ACLs, both `s3:CreateBucket` and
  `s3:PutBucketAcl` permissions are required. In your `CreateBucket`
  request, if you set the ACL to `private`, or if you don't specify any
  ACLs, only the `s3:CreateBucket` permission is required." rgw-go takes an
  `x-amz-acl` other than `private`, and any `x-amz-grant-*` header, the
  headers radosgw builds the bucket's ACL from, for an ACL that needs
  s3:PutBucketAcl; it evaluates the action as it evaluates the object-lock
  actions, on the new bucket's ARN against the requester's own policies, and
  answers a denial with 403 AccessDenied, creating nothing. The canned ACLs
  the page does not name, `bucket-owner-read` and
  `bucket-owner-full-control`, need it too, as other custom ACLs, and so
  does a grant header that names the requester alone. rgw-go makes the
  check before it finds that the bucket exists, as Amazon S3 does, since the
  rule follows the headers the request sends: a denied requester
  re-creating a bucket it already owns with such an ACL gets 403 and the
  bucket is left as it was. radosgw answers that re-create 200 when the ACL
  is the bucket's own
  ([rgw_rest_s3.cc:2554-2555](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2554-L2555),
  [rgw_rest_s3.cc:2707-2708](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2707-L2708)),
  and 409 BucketAlreadyExists, "Cannot modify existing access control
  policy", when it is another
  ([rgw_op.cc:3569-3578](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3569-L3578),
  [rgw_op.cc:3797-3806](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3797-L3806)).
- **CreateBucket with x-amz-object-ownership is authorized as
  s3:PutBucketOwnershipControls too.** Neither floor reads the header: a
  floor radosgw creates the bucket with no ownership setting, whatever the
  header says
  ([rgw_rest_s3.cc:2481-2550](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2481-L2550),
  [rgw_rest_s3.cc:2593-2703](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2593-L2703)).
  v21.1.2 reads it in `get_params`, refusing a value it does not parse with
  400 InvalidArgument
  ([rgw_rest_s3.cc:2809-2816](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L2809-L2816)), and stores
  the setting in the new bucket's attrs
  ([rgw_op.cc:4233-4237](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L4233-L4237)). Its
  `verify_permission` checks s3:PutBucketOwnershipControls against the
  value before `get_params` sets it, so the check never refuses
  ([rgw_op.cc:3754-3759](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L3754-L3759),
  [rgw_op.cc:4067](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L4067); `docs/ceph-upstream-bugs.md`,
  "[radosgw's CreateBucket checks s3:PutBucketOwnershipControls before it
  reads
  x-amz-object-ownership](ceph-upstream-bugs.md#radosgws-createbucket-checks-s3putbucketownershipcontrols-before-it-reads-x-amz-object-ownership)").
  Amazon S3's CreateBucket reference, retrieved 2026-10-21: "If your
  `CreateBucket` request includes the `x-amz-object-ownership` header, then
  the `s3:PutBucketOwnershipControls` permission is required." rgw-go stores
  the setting as v21.1.2 does. Whatever the setting, it evaluates
  s3:PutBucketOwnershipControls as it evaluates the object-lock actions, on
  the new bucket's ARN against the requester's own policies, and answers a
  denial with 403 AccessDenied, creating nothing, before it finds that the
  bucket exists. Amazon S3's User Guide exempts one value: "No additional
  permissions are needed when creating a bucket with the Object writer
  setting applied"
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-ownership-new-bucket.html,
  retrieved 2026-10-23), where the API reference asks for the permission
  whenever the header is sent. rgw-go follows the API reference, the
  operation's contract. An ObjectWriter the header names is stored as the
  other two settings are, and afterwards only a holder of the action may
  change it, through PutBucketOwnershipControls. A requester denied the
  action loses nothing by leaving the header out, since a bucket without a
  setting acts as ObjectWriter.

  It also refuses BucketOwnerEnforced beside an ACL other than the default
  private one with 400 InvalidBucketAclWithObjectOwnership, creating
  nothing, again before it finds that the bucket exists: the requester's
  re-create of its own bucket with the ACL that bucket holds is 400 where
  v21.1.2 answers 200. Amazon S3's reference says "To set an ACL on a bucket
  as part of a CreateBucket request, you must explicitly set S3 Object
  Ownership for the bucket to a different value than the default,
  BucketOwnerEnforced"
  ([api.go:620-623](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L620-L623)),
  and its troubleshooting page names the code: "if your `CreateBucket`
  request sets Bucket owner enforced and specifies a bucket ACL that
  provides access to an external AWS account, your request fails with a
  `400` error and returns the InvalidBucketAclWithObjectOwnership error code"
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-ownership-error-responses.html,
  retrieved 2026-10-23). Amazon S3's message is "Bucket cannot have ACLs set
  with ObjectOwnership's BucketOwnerEnforced setting"; rgw-go sends the one
  v21.1.2's PutBucketOwnershipControls gives such an ACL, "The bucket's ACL
  must be made private before setting BucketOwnerEnforced.". The ACL is held
  to what PutBucketOwnershipControls holds it to ("rgw-go stores and enforces
  ownership controls a floor radosgw ignores", below), so a bucket under
  BucketOwnerEnforced always holds a private ACL. `bucket-owner-full-control`
  and `bucket-owner-read`, which CreateBucket otherwise builds with a grant
  to an empty bucket owner, are built under the setting as the private ACL,
  an ACL radosgw reads as any other: Amazon S3's canned ACL table says of
  each, "If you specify this canned ACL when creating a bucket, Amazon S3
  ignores it"
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#canned-acl,
  retrieved 2026-10-23). Each still needs s3:PutBucketAcl ("CreateBucket with
  an ACL other than private is authorized as s3:PutBucketAcl too", above).
  The private ACL is a new bucket's alone: a re-create compares either canned
  ACL as radosgw builds it, so the owner's re-create of its private bucket
  with either is 409 BucketAlreadyExists, "Cannot modify existing access
  control policy", and its re-create of a bucket that holds radosgw's ACL for
  it is 200, as radosgw answers both
  ([rgw_op.cc:4194-4210](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L4194-L4210)).
  v21.1.2 creates a BucketOwnerEnforced bucket with whatever ACL its headers
  built (`docs/ceph-upstream-bugs.md`, "[v21's CreateBucket stores an ACL
  other than private on a bucket it makes
  BucketOwnerEnforced](ceph-upstream-bugs.md#v21s-createbucket-stores-an-acl-other-than-private-on-a-bucket-it-makes-bucketownerenforced)").
  A re-create of the requester's own bucket that names another setting is
  answered as having existed and changes nothing, as v21.1.2 answers it,
  since its existing-bucket checks compare no setting. The S3 handler does
  not read the header yet, so no request reaches either check until it
  does.
- **A requested bucket index answers 501 on Tentacle.** Tentacle's
  CreateBucket reads a `BucketIndex` element of `CreateBucketConfiguration`,
  whose `Type` and `NumShards` set the new bucket's index, and refuses a
  re-create that asks for another index (`rgw_rest_s3.cc:2651-2684` and
  `driver/rados/rgw_sal_rados.cc:195-201` at v20.2.4). rgw-go checks the
  element as radosgw does, answering a malformed one with radosgw's 400
  InvalidArgument and message. A valid one on a name no bucket holds is 501
  NotImplemented, creating nothing, once every check radosgw makes before
  it creates a bucket has passed: the object-lock header, the location
  constraint, the placement and the existing-bucket checks. A name that
  holds a bucket is answered as radosgw answers it whatever the index asks
  for: 409 for another owner's, 200 for the requester's own. The 501
  includes a `BucketIndex` naming a Normal index without `NumShards`,
  which on a Normal placement asks for what an ordinary create gives.
  Squid reads no such element.
- **GetObjectAttributes is served on Squid too.** A Squid radosgw has no
  GetObjectAttributes operation and answers `GET ?attributes` as a
  GetObject, with the object's body (`RGWHandler_REST_Obj_S3::op_get`,
  `rgw_rest_s3.cc:4805-4821` at v19.2.6); rgw-go serves the operation on
  both releases, as the Tentacle feature level for gateway-side features
  asks, so on a Squid zone the same request returns the attributes
  document from rgw-go and the object from radosgw ("Request parsing and
  dispatch differences", "On Squid, `?attributes` is GetObjectAttributes").
  The s3-tests case `test_get_object_attributes` is recorded as failed
  against Squid's radosgw (`test/s3tests/baseline/squid.json`); against
  rgw-go on Squid it exercises the operation instead.
- **BitTorrent files are not served.** `?torrent` answers 404 NoSuchKey
  for an object that has no torrent, as radosgw does, and 501
  NotImplemented for one that carries `user.rgw.torrent`, where radosgw
  returns the bencoded file. Ceph's S3 compliance table lists GET Object
  torrent as unsupported (`doc/dev/radosgw/s3_compliance.rst:250`,
  "Operations on Objects", at v19.2.6 and v20.2.4), but the code serves a
  stored torrent: a PUT generates one only while `rgw_torrent_flag` is on,
  and the option defaults to false (`RGWPutObj::get_torrent_filter`,
  `rgw_op.cc:4110-4125` at v19.2.6, `:4319-4334` at v20.2.4;
  `common/options/rgw.yaml.in:3205-3210` at v19.2.6, `:3343-3348` at
  v20.2.4), and `GET ?torrent`, authorized as `s3:GetObjectTorrent` or
  `s3:GetObjectVersionTorrent` (`rgw_op.cc:989-994` at v19.2.6,
  `:1195-1196` at v20.2.4), returns the stored file (`:2276-2292` at
  v19.2.6, `:2512-2528` at v20.2.4). On a zone with the default
  configuration the two gateways therefore agree, 404 NoSuchKey; they
  differ only for objects written while an operator had the flag on, which
  Rook's operator does not set (rook/rook at 9f8960d3d). radosgw also reads
  a torrent that an older release kept in the head object's omap ("Object
  read differences", "Torrents"); rgw-go does not.

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
- **The Swift and zero APIs answer 405.** When `rgw_enable_apis` names
  `swift`, `swift_auth` or `zero`, radosgw serves that API at
  `rgw_swift_url_prefix`, `rgw_swift_auth_entry` or `zero`
  (`rgw_appmain.cc:307-366` at v19.2.6, `:316-375` at v20.2.4). rgw-go
  serves none of them. It keeps every path at or under each of those
  entries, and under each parent `register_resource` adds for a nested
  entry, a nested `rgw_admin_entry`'s included, from the S3 handler
  (`rgw_rest.cc:1935-1966` at v19.2.6, `:1952-1983` at v20.2.4), matching
  them as radosgw does against the path with a virtual-hosted bucket in
  front (`:2154-2160` and `:2182` at v19.2.6, `:2171-2177` and `:2204` at
  v20.2.4), so a virtual-hosted key such as `swift/x` stays S3's. It
  answers each request there as radosgw answers one no handler takes: 405
  MethodNotAllowed, or 400 InvalidRequest for a NUL in the path
  (`:2182-2186` and `:2290-2304` at v19.2.6, `:2204-2208` and
  `:2312-2326` at v20.2.4). With `rgw_swift_url_prefix` set to `/`,
  radosgw serves no S3 and Swift at every path; rgw-go answers 405 to
  every request outside the admin API, as it does when `rgw_enable_apis`
  names neither `s3` nor `s3website`.
- **The CNAME fallback resolves no CNAME.** When the Host matches none of
  its hostnames and `rgw_resolve_cname` is set (it defaults to false),
  radosgw's `resolve_cname` looks up the Host's CNAME, which preprocess
  matches instead
  ([rgw_rest.cc:2091-2130](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2091-L2130),
  [rgw_rest.cc:2103-2142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2103-L2142)),
  against the S3 hostnames and, with `s3website` enabled, the website
  ones, where a match makes the request a website one.
  rgw-go matches the Host itself, never its CNAME, and reads it as radosgw
  reads a Host that matches nothing: one that looks like no IP address and
  passes `validate_bucket_name` is the bucket whole when any hostname is
  configured, the website ones included, `rgw_dns_s3website_name` and the
  zonegroup's `hostnames_s3website`
  ([rgw_rest.cc:235-238](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L235-L238),
  [rgw_rest.cc:235-238](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L235-L238);
  [rgw_rest.cc:2141-2148](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2141-L2148),
  [rgw_rest.cc:2153-2160](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2153-L2160)),
  and makes a website request only when `s3website` ranks above `s3`.
  SigV2 signs that bucket in front of the path, as radosgw signs the
  `request_uri` that `RGWREST::preprocess` rewrote
  ([rgw_auth_s3.cc:249-254](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L249-L254),
  [rgw_auth_s3.cc:251-256](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L251-L256)),
  so a SigV2 request on a Host the two read differently is signed over
  another resource in rgw-go and fails there with 403
  SignatureDoesNotMatch.
- **On Squid, a request with no Host is a website request when `s3website`
  outranks `s3`.** radosgw ranks each name of `rgw_enable_apis` by its
  first place, an absent one below any named
  ([rgw_rest.cc:2038-2051](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2038-L2051),
  [rgw_rest.cc:2050-2063](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2050-L2063)),
  and with `s3website` above `s3` sets `RGW_REST_WEBSITE`, the website
  flag, on every request with a Host
  ([rgw_rest.cc:2150-2156](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2150-L2156),
  [rgw_rest.cc:2162-2168](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2162-L2168)).
  Tentacle's sets `RGW_REST_WEBSITE` on a request with no Host too
  ([rgw_rest.cc:2193-2198](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2193-L2198));
  Squid's serves that request as S3. A request has no Host when its Host
  header is absent, empty or a port alone, which `req_info` drops
  ([rgw_common.cc:246-261](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L246-L261),
  [rgw_common.cc:256-271](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L256-L271)).
  rgw-go makes it a website request on both releases, the Tentacle feature
  level for gateway-side request parsing (`docs/ceph-upstream-bugs.md`,
  "[Squid's radosgw serves a request with no Host as S3 when s3website
  outranks
  s3](ceph-upstream-bugs.md#squids-radosgw-serves-a-request-with-no-host-as-s3-when-s3website-outranks-s3)").
- **The hostnames are read at startup only.** rgw-go reads `rgw_dns_name`,
  `rgw_dns_s3website_name` and its zonegroup's `hostnames` and
  `hostnames_s3website` once, when it starts (`s3Config`,
  `internal/cli/serve.go`), and has no realm reload, so a period commit that
  changes the zonegroup's hostnames takes effect at its next restart. radosgw
  reloads its realm on a period commit and runs `rgw_rest_init` again
  ([rgw_realm_reloader.cc:181](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_realm_reloader.cc#L181),
  [rgw_realm_reloader.cc:186](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_realm_reloader.cc#L186)),
  which inserts the period's names into two static sets it never clears
  ([rgw_rest.cc:182-183](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L182-L183),
  [rgw_rest.cc:182-183](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L182-L183);
  [rgw_rest.cc:215-238](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L215-L238),
  [rgw_rest.cc:215-238](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L215-L238)), so it
  adds a name on a reload and keeps a removed one until it restarts
  (`docs/ceph-upstream-bugs.md`, "[radosgw keeps the hostnames a realm
  reload drops until it
  restarts](ceph-upstream-bugs.md#radosgw-keeps-the-hostnames-a-realm-reload-drops-until-it-restarts)").
- **On Squid, `?attributes` is GetObjectAttributes.** The object
  `op_get` of Squid's radosgw has no attributes branch
  (`rgw_rest_s3.cc:4805-4821` at v19.2.6), so `GET /b/k?attributes` is
  GetObject there and returns the object's body; Tentacle's runs
  GetObjectAttributes (`:5370-5371` at v20.2.4). rgw-go routes the
  request to GetObjectAttributes on both releases, the Tentacle feature
  level. On Squid it still accepts the signed payload forms of the
  GetObject a Squid radosgw would run, so authentication refuses no
  request radosgw takes.
- **ownershipControls requests are served as v21 serves them.** Neither
  floor has the ownership controls ops: no `op_*` branch tests
  `ownershipControls`, and `RGWHTTPArgs::append` keeps it as no
  sub-resource
  ([rgw_common.cc:927-951](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L927-L951),
  [rgw_common.cc:940-964](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L940-L964)).
  So each request falls through its method's branches
  ([rgw_rest_s3.cc:4660-4808](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4660-L4808),
  [rgw_rest_s3.cc:5171-5326](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5171-L5326)):
  a floor radosgw serves GetBucketOwnershipControls as ListObjects, or
  ListObjectsV2 with `list-type=2`; PutBucketOwnershipControls as
  CreateBucket, which refuses the document an SDK sends with 400
  InvalidArgument for naming no `CreateBucketConfiguration`
  ([rgw_rest_s3.cc:2466-2469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2466-L2469),
  [rgw_rest_s3.cc:2637-2642](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2637-L2642);
  v19.2.7's `get_params` answers it when `get_location_constraint` fails,
  [rgw_rest_s3.cc:2525-2528](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2525-L2528)),
  and creates a bucket under a free name for an empty body; and
  DeleteBucketOwnershipControls as DeleteBucket, which deletes an empty
  bucket (docs/ceph-upstream-bugs.md, "[radosgw serves bucket configuration
  requests it has no op for as ListObjects, CreateBucket and DeleteBucket,
  deleting an empty
  bucket](ceph-upstream-bugs.md#radosgw-serves-bucket-configuration-requests-it-has-no-op-for-as-listobjects-createbucket-and-deletebucket-deleting-an-empty-bucket)").
  rgw-go routes the three on both releases as v21.1.2, the first release
  line to carry the ops, does: by the key's presence, whatever its value,
  after each method's encryption branch, and on DELETE ahead of the website
  and mdsearch ones
  ([rgw_rest_s3.h:772-774](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.h#L772-L774);
  [rgw_rest_s3.cc:5469-5474](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5469-L5474),
  [rgw_rest_s3.cc:5529-5534](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5529-L5534),
  [rgw_rest_s3.cc:5556-5576](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L5556-L5576)).
  A request whose query names an earlier branch's key is still served by
  that branch, and a HEAD, a POST or an object's request with the key is
  served as before, as v21.1.2 serves it. Being ahead of the website and
  mdsearch branches, the DELETE also takes two requests a floor serves
  otherwise: `?website&ownershipControls`, which a floor serves as
  DeleteBucketWebsite, or refuses with 405 MethodNotAllowed before
  authentication while `rgw_enable_static_website` is off; and
  `?mdsearch&ownershipControls`, which a floor serves as
  `RGWDelBucketMetaSearch`, at v20.2.4 only while `rgw_enable_mdsearch` is
  on. rgw-go answered that one 501 through its metadata search route, and
  still does. SigV2 signs the key on no release, v21.1.2 included. As
  v21.1.2's whitelist has it, the PUT takes a single-chunk signed payload
  and the GET and DELETE neither form
  ([rgw_rest_s3.cc:6793-6842](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L6793-L6842)).
  Those are the forms the floors' CreateBucket, ListObjects and DeleteBucket
  take, so authentication accepts and refuses each request naming the key
  alone as it did before. DeleteBucketWebsite takes a single chunk, so a
  `?website&ownershipControls` DELETE with a signed, non-empty body is now
  refused by authentication with 501 NotImplemented. Until rgw-go serves
  the ops, each of the three answers 501 NotImplemented after
  authentication, so none lists, creates or deletes a bucket on either
  release. The PUT's document is read as "A PutBucketOwnershipControls
  document is held to S3's schema" records, not as v21.1.2 reads it.
- **A bucket or object request radosgw has no op for answers 501.** No
  release up to v21.1.2 has an op for Amazon S3's bucket analytics,
  intelligent-tiering, inventory, metrics and metadata configurations, for
  an object's annotations, or for RenameObject and UpdateObjectEncryption:
  no `op_*` branch tests `analytics`, `intelligent-tiering`, `inventory`,
  `metrics`, `metadataConfiguration`, `metadataTable`, `annotation`,
  `renameObject` or, at object scope, `encryption`, and none is a
  sub-resource. So a floor radosgw serves each request as its method's
  fall-through. A bucket GET is ListObjects, or ListObjectsV2 with
  `list-type=2`; a PUT is CreateBucket, which creates a bucket under a free
  name for an empty body; and a DELETE is DeleteBucket, which deletes an
  empty bucket
  ([rgw_rest_s3.cc:4640-4808](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4640-L4808),
  [rgw_rest_s3.cc:5151-5326](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5151-L5326)).
  The object `op_get`, `op_put` and `op_delete`
  ([rgw_rest_s3.cc:4835-4892](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4835-L4892),
  [rgw_rest_s3.cc:5360-5419](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5360-L5419))
  serve a GET as GetObject; a PUT as PutObject, which replaces the object's
  data with the request's body, an empty one for RenameObject, or as
  CopyObject with a copy source; and a DELETE as DeleteObject, which deletes
  the object, or the version `versionId` names (docs/ceph-upstream-bugs.md,
  "[radosgw serves bucket configuration requests it has no op for as
  ListObjects, CreateBucket and DeleteBucket, deleting an empty
  bucket](ceph-upstream-bugs.md#radosgw-serves-bucket-configuration-requests-it-has-no-op-for-as-listobjects-createbucket-and-deletebucket-deleting-an-empty-bucket)",
  "[radosgw serves PutObjectAnnotation and DeleteObjectAnnotation as
  PutObject and DeleteObject, overwriting or deleting the
  object](ceph-upstream-bugs.md#radosgw-serves-putobjectannotation-and-deleteobjectannotation-as-putobject-and-deleteobject-overwriting-or-deleting-the-object)"
  and "[radosgw serves RenameObject and UpdateObjectEncryption as
  PutObject, overwriting the
  object](ceph-upstream-bugs.md#radosgw-serves-renameobject-and-updateobjectencryption-as-putobject-overwriting-the-object)").
  On both releases rgw-go routes each of the six bucket keys on GET, PUT
  and DELETE, `annotation` on an object's GET, PUT and DELETE, and
  `renameObject` and `encryption` on an object's PUT, to a route of its
  own, after every branch radosgw has and just before the method's
  fall-through, and answers 501 NotImplemented: it lists, reads, creates,
  overwrites and deletes nothing. Spec §8's compatibility "does not mean
  reproducing radosgw's defects", and a 501 writes nothing to the RADOS
  state the two gateways share. A request that also names an earlier
  branch's key is served by that branch, as before. The routes take neither
  signed payload form, so authentication now refuses, with the same 501
  and before it looks the key up, the payloads the fall-through took: a
  bucket PUT's signed single-chunk body, an object PUT's signed
  single-chunk or aws-chunked one, and an object GET's signed, non-empty
  one. AWS's requests are those aws-sdk-go-v2's S3 client v1.114.3 sends;
  `metadataConfiguration` and `metadataTable` have no PUT there, their
  Create being a POST, which rgw-go routes to `post_obj` as radosgw routes
  it to its form upload.
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
- **A PutPublicAccessBlock document is held to S3's schema.** radosgw
  looks up a mandatory `PublicAccessBlockConfiguration` root and its four
  optional booleans, and answers 400 MalformedXML only to a body expat
  cannot parse or one without that root
  ([rgw_op.cc:8646-8658](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8646-L8658),
  [rgw_op.cc:9614-9626](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9614-L9626);
  [rgw_public_access.cc:4-9](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.cc#L4-L9),
  [rgw_public_access.cc:4-9](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.cc#L4-L9)).
  Past that it reads the body leniently. It ignores an element it does not
  know, so a misspelled `BlockPublicACLs` or a prefixed `s3:BlockPublicAcls`
  leaves that boolean false while the request answers 200
  (docs/ceph-upstream-bugs.md, "[radosgw stores false for a public access
  block setting it cannot
  find](ceph-upstream-bugs.md#radosgw-stores-false-for-a-public-access-block-setting-it-cannot-find)").
  Of a repeated boolean it reads the first and never decodes the others
  ([rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241),
  [rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L218-L241)).
  It ignores attributes, `xmlns` among them, so any namespace is taken,
  and text between the elements. It expands the entities a document type
  declaration declares (docs/ceph-upstream-bugs.md, "[radosgw's XML parser
  expands the entities a request body
  declares](ceph-upstream-bugs.md#radosgws-xml-parser-expands-the-entities-a-request-body-declares)").
  It reads a boolean as `true` or `false` in any ASCII case, or else as a
  decimal integer with whitespace around it, non-zero for true
  ([rgw_xml.cc:392-406](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L392-L406),
  [rgw_xml.cc:392-406](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L392-L406);
  [rgw_xml.cc:282-308](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L282-L308),
  [rgw_xml.cc:282-308](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L282-L308)).
  Amazon S3 answers MalformedXML to "XML that doesn't conform to the
  published XSD" for a configuration, and its PutPublicAccessBlock takes a
  required root in S3's namespace holding four booleans (S3's error list
  and the operation's input as the AWS SDK for Go carries them,
  [api.go:20217-20221](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20217-L20221),
  [api.go:39151](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L39151),
  [api.go:33710-33751](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L33710-L33751)).
  AWS's answer to each case below was not checked against Amazon S3
  itself. rgw-go answers 400 MalformedXML (`acl.ParsePublicAccessBlockXML`,
  `internal/acl/publicaccess.go`) to:
  - a document type declaration, or any other `<!` declaration;
  - a root other than an unprefixed `PublicAccessBlockConfiguration`, or a
    second root;
  - under the root, an element other than the four booleans, a prefixed
    one even when its prefix is bound to S3's namespace, an element inside
    a boolean, and a repeated boolean;
  - an attribute other than an `xmlns` naming S3's namespace or none,
    prefix declarations included;
  - text other than XML whitespace (space, tab, carriage return and line
    feed) outside a boolean;
  - an integer other than `1` or `0` as a boolean, such as `2`, `-7`, `+0`
    or `01`, which radosgw's integer fallback reads, non-zero as true, and
    which no schema gives a boolean meaning. rgw-go reads `true` and
    `false` in any ASCII case, as radosgw does and as AWS's reference
    writes `TRUE`, and `1` or `0` with XML whitespace around it, as
    xs:boolean and radosgw both read them. It refuses whitespace around
    `true` or `false`, as radosgw does. Both read a value with its
    references and CDATA sections decoded and its comments and processing
    instructions dropped;
  - a boolean whose text, once decoded, is longer than 64 bytes, which
    radosgw reads when it is `1` or `0` inside whitespace;
  - a body longer than 1 MiB, `rgw_max_put_param_size`'s default
    ([rgw.yaml.in:138-145](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145),
    [rgw.yaml.in:144-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151)).
    radosgw's op reads the body with that option as its `max_len` and
    refuses a Content-Length over it unread, `-ERANGE`
    ([rgw_rest.cc:1556-1558](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1556-L1558),
    [rgw_rest.cc:1556-1558](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1556-L1558)),
    which it answers 416 InvalidRange
    ([rgw_common.cc:128](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L128),
    [rgw_common.cc:129](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L129)),
    so under the default the parser's limit is never reached. Under a
    raised option radosgw reads a longer document, and rgw-go refuses it.

  The refusal comes at the first token that breaks a rule, so a hostile
  document costs no more than its offending prefix. A document an SDK sends
  reads alike on both gateways, and both store the block in the same
  `user.rgw.public-access` encoding.
- **A PutBucketEncryption document is held to S3's schema and AWS's
  values.** radosgw looks up a mandatory `ServerSideEncryptionConfiguration`
  root and, under it, an optional `Rule` holding an optional
  `ApplyServerSideEncryptionByDefault`, with its optional `SSEAlgorithm`
  and `KMSMasterKeyID`, and an optional `BucketKeyEnabled`. It answers 400
  MalformedXML only to a body expat cannot parse, one without that root,
  and a `BucketKeyEnabled` it cannot read as a boolean
  ([rgw_op.cc:8774-8796](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8774-L8796),
  [rgw_op.cc:9742-9764](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9742-L9764);
  [rgw_bucket_encryption.cc:8-34](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket_encryption.cc#L8-L34),
  [rgw_bucket_encryption.cc:8-34](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket_encryption.cc#L8-L34)).
  Past that it reads the body leniently. It ignores an element it does not
  know or does not find in its place, so a misspelled `SSEAlgorithm` or
  `Rule`, or AWS's `BlockedEncryptionTypes`, is dropped while the request
  answers 200, and of a repeated element it reads the first
  (docs/ceph-upstream-bugs.md, "[radosgw's PutBucketEncryption stores a
  configuration without the elements it cannot
  find](ceph-upstream-bugs.md#radosgws-putbucketencryption-stores-a-configuration-without-the-elements-it-cannot-find)").
  It ignores attributes, `xmlns` among them, and text between the
  elements, and it expands the entities a document type declaration
  declares. It reads `BucketKeyEnabled` as `true` or `false` in any ASCII
  case, or else as a decimal integer with whitespace around it, non-zero
  for true
  ([rgw_xml.cc:392-406](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L392-L406),
  [rgw_xml.cc:392-406](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L392-L406)).
  And it stores any `SSEAlgorithm`, and a `KMSMasterKeyID` beside any
  algorithm (docs/ceph-upstream-bugs.md, "[radosgw's PutBucketEncryption
  stores any SSEAlgorithm, and a KMS key id with any
  algorithm](ceph-upstream-bugs.md#radosgws-putbucketencryption-stores-any-ssealgorithm-and-a-kms-key-id-with-any-algorithm)").
  Amazon S3 takes an `SSEAlgorithm` of `AES256`, `aws:kms`,
  `aws:kms:dsse`, `aws:fsx` or `aws:backup`, and a `KMSMasterKeyID` "if
  and only if SSEAlgorithm is set to aws:kms or aws:kms:dsse"
  (https://docs.aws.amazon.com/AmazonS3/latest/API/API_ServerSideEncryptionByDefault.html;
  the model the AWS SDK for Go v2 carries,
  [enums.go:1469-1478](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.2/service/s3/types/enums.go#L1469-L1478)
  and
  [types.go:4845-4898](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.2/service/s3/types/types.go#L4845-L4898)),
  and answers MalformedXML to "XML that doesn't conform to the published
  XSD" for a configuration (S3's error list as the AWS SDK for Go carries
  it,
  [api.go:20217-20221](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20217-L20221)).
  AWS's answer to each case below was not checked against Amazon S3
  itself. rgw-go answers 400 MalformedXML (`sse.ParseXML`,
  `internal/sse/xml.go`) to:
  - a document type declaration, or any other `<!` declaration;
  - a root other than an unprefixed `ServerSideEncryptionConfiguration`,
    or a second root;
  - an element the schema does not place where it stands: an unknown or
    misspelled one, `BlockedEncryptionTypes`, one under another parent
    than its own, and a prefixed one even when its prefix is bound to S3's
    namespace; an element inside a value; and a repeated element, a
    second `Rule` among them;
  - an attribute other than an `xmlns` naming S3's namespace or none,
    prefix declarations included;
  - text other than XML whitespace (space, tab, carriage return and line
    feed) outside a value;
  - a `BucketKeyEnabled` other than `true` or `false` in any ASCII case,
    or `1` or `0` with XML whitespace around it, so an integer such as
    `2`, `-7`, `+0` or `01`, which radosgw reads, non-zero as true; and one
    whose text, once decoded, is longer than 64 bytes;
  - an `SSEAlgorithm` outside AWS's five, compared as written, so `aes256`
    and ` AES256` too, and a `KMSMasterKeyID` with an algorithm other than
    `aws:kms` or `aws:kms:dsse` (`sse.BucketEncryptionConfig.Validate`);
  - a body longer than 1 MiB, `rgw_max_put_param_size`'s default
    ([rgw.yaml.in:138-145](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145),
    [rgw.yaml.in:144-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151)).
    radosgw's op reads the body with that option as its `max_len` and
    refuses a Content-Length over it unread, `-ERANGE`
    ([rgw_rest.cc:1556-1558](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1556-L1558),
    [rgw_rest.cc:1556-1558](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1556-L1558)),
    which it answers 416 InvalidRange
    ([rgw_common.cc:128](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L128),
    [rgw_common.cc:129](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L129)),
    so under the default the parser's limit is never reached. Under a
    raised option radosgw reads a longer document, and rgw-go refuses it.

  rgw-go keeps radosgw's reading where S3's model is stricter and nothing
  the document says is lost: a document without a `Rule` stores a
  configuration without one, and a `Rule` without an `SSEAlgorithm` stores
  one naming no algorithm, though the model marks both required
  ([types.go:4901-4910](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.2/service/s3/types/types.go#L4901-L4910),
  [types.go:4845-4853](https://github.com/aws/aws-sdk-go-v2/blob/service/s3/v1.114.2/service/s3/types/types.go#L4845-L4853)).
  An empty `KMSMasterKeyID` is no key id, as both gateways store it. A
  value is its element's text, references and CDATA decoded, comments and
  processing instructions dropped, nothing trimmed, as radosgw reads it.
  The refusal comes at the first token that breaks a rule, so a hostile
  document costs no more than its offending prefix. An SDK document that
  names one of AWS's algorithms and carries no `BlockedEncryptionTypes`
  reads alike on both gateways, and both store it in the same
  `user.rgw.sse-s3.policy` bytes. An SDK sends an algorithm outside AWS's
  list as it is given, since it does not check the enum, and
  `BlockedEncryptionTypes` when asked to, as s3-tests'
  `_put_bucket_blocked_encryption_types` does: radosgw stores the first
  and drops the second, and rgw-go refuses both.
- **A PutBucketOwnershipControls document is held to S3's schema.** Neither
  floor has the op, so v21.1.2, the first release line to carry it, is the
  reading compared. radosgw looks up a mandatory `OwnershipControls` root,
  its first `Rule` and that `Rule`'s first `ObjectOwnership`, whose text
  must be `BucketOwnerEnforced`, `BucketOwnerPreferred` or `ObjectWriter`
  as written. It answers 400 MalformedXML with no message to a body expat
  cannot parse, and, once expat has accepted the whole body, to a missing
  root, `Rule` or `ObjectOwnership`, or another name, each with its own
  message
  ([rgw_rest_s3.cc:4553-4579](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4553-L4579);
  [rgw_object_ownership.cc:36-69](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_object_ownership.cc#L36-L69)).
  Past that it reads the body leniently. It ignores an element it does not
  look up, and of a repeated `Rule` or `ObjectOwnership` it reads the first
  ([rgw_xml.cc:147-153](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_xml.cc#L147-L153)).
  It keeps attributes, `xmlns` among them, without reading them
  ([rgw_xml.cc:66-75](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_xml.cc#L66-L75)),
  so any namespace is taken; it ignores text between the elements; and it
  expands the entities a document type declaration declares
  (docs/ceph-upstream-bugs.md, "[radosgw's XML parser expands the entities
  a request body
  declares](ceph-upstream-bugs.md#radosgws-xml-parser-expands-the-entities-a-request-body-declares)").
  Amazon S3 answers MalformedXML to "XML that doesn't conform to the
  published XSD" for a configuration, and its PutBucketOwnershipControls
  takes a required root in S3's namespace holding a required list of
  `Rule` elements, each with a required `ObjectOwnership` (S3's error list
  and the operation's input as the AWS SDK for Go carries them,
  [api.go:20217-20221](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20217-L20221),
  [api.go:35881](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L35881),
  [api.go:33255-33262](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L33255-L33262),
  [api.go:33312-33342](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L33312-L33342)).
  AWS's answer to each case below was not checked against Amazon S3
  itself. rgw-go answers 400 MalformedXML (`acl.ParseOwnershipXML`,
  `internal/acl/ownership.go`) to:
  - a document type declaration, or any other `<!` declaration;
  - under the root, an element other than `Rule`; under `Rule`, one other
    than `ObjectOwnership`; any element inside `ObjectOwnership`; and a
    prefixed one, even when its prefix is bound to S3's namespace;
  - a repeated `ObjectOwnership`, and a second `Rule`, though AWS's model
    types `Rule` as a list, so a document an SDK builds with two rules is
    refused where radosgw reads the first;
  - an attribute other than an `xmlns` naming S3's namespace or none,
    prefix declarations included;
  - text other than XML whitespace (space, tab, carriage return and line
    feed) outside `ObjectOwnership`;
  - a body longer than 1 MiB, `rgw_max_put_param_size`'s default
    ([rgw.yaml.in:164-171](https://github.com/ceph/ceph/blob/v21.1.2/src/common/options/rgw.yaml.in#L164-L171)).
    radosgw's op reads the body with that option as its `max_len` and
    refuses a Content-Length over it unread, `-ERANGE`
    ([rgw_rest.cc:1527-1529](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest.cc#L1527-L1529)),
    which it answers 416 InvalidRange
    ([rgw_common.cc:125](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_common.cc#L125)).
    A chunked body, which has no Content-Length, radosgw reads past the
    option. The op asks for no chunked body, `read_all_input(s, max_size,
    false)`
    ([rgw_rest_s3.cc:4557](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4557)),
    but `RGWOp::read_all_input` drops that argument and reads one
    ([rgw_op.h:229-241](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.h#L229-L241);
    docs/ceph-upstream-bugs.md, "[RGWOp::read_all_input ignores its
    allow_chunked
    argument](ceph-upstream-bugs.md#rgwopread_all_input-ignores-its-allow_chunked-argument)"),
    and `read_all_chunked_input` checks the option only after a read fills
    its buffer
    ([rgw_rest.cc:1478-1512](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest.cc#L1478-L1512)),
    so under the 1 MiB default radosgw reads and parses a chunked body of up
    to 1,175,551 bytes. rgw-go's `op.ReadParamBody` reads such a body as
    radosgw does (`internal/op/parambody.go`), and `acl.ParseOwnershipXML`
    then refuses one over 1 MiB. Under a raised option radosgw reads a
    longer document of either kind, and rgw-go refuses it.

  Two of rgw-go's refusals carry another message than radosgw's, under the
  same 400 MalformedXML:
  - a refusal for one of the rules above carries no message, also where
    radosgw refuses the same body with one: an unknown element standing
    where `Rule` or `ObjectOwnership` belongs, such as `<Rules>` or
    `<Rule><ID>x</ID></Rule>`, is refused as outside the schema, where
    radosgw sends "Missing required element Rule" or "Missing required
    element ObjectOwnership";
  - a root other than an unprefixed `OwnershipControls` is refused at its
    start tag with radosgw's "Missing required element OwnershipControls",
    even when expat would refuse the body, where radosgw sets that message
    only once expat has accepted the whole body
    ([rgw_rest_s3.cc:4566-4574](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_rest_s3.cc#L4566-L4574)).
    The scan stops at the root's start tag so as not to read a foreign
    root's content, so the message is sent once Go's decoder has read
    that tag, whatever expat makes of the body: a malformation after the
    root, as in `<Foo/>junk` or an unclosed `<Foo>`; one before it that
    Go's decoder passes, as an XML declaration after whitespace,
    ` <?xml version="1.0"?><Foo/>`, or a processing instruction with a bad
    target, `<?p*?><Foo/>`; and one on the root tag that it passes, as a
    repeated attribute, `<Foo a="1" a="1"/>`. A malformation Go's decoder
    meets first, before or on the root's start tag, is refused with no
    message, as radosgw refuses it: a comment holding two hyphens,
    `<!-- a -- b --><Foo/>`; a NUL byte before the root; an unquoted
    attribute value, `<Foo a=1/>`, or one holding a less-than sign,
    `<Foo a="<"/>`; and a root name that starts with a digit, `<1Foo/>`.

  rgw-go keeps radosgw's reading of the name: compared as written, nothing
  trimmed, its references and CDATA decoded and its comments and
  processing instructions dropped. The refusal comes at the first token
  that breaks a rule, so a hostile document costs no more than its
  offending prefix. A document an SDK sends with one rule reads alike on
  both gateways, and both store the setting in the same
  `user.rgw.ownership-controls` bytes.
- **A NUL in a request argument that names a RADOS object is refused.**
  radosgw refuses a NUL only in the decoded path (`ERR_ZERO_IN_URL`,
  `rgw_rest.cc:2182-2186` at v19.2.6, `:2204-2208` at v20.2.4). A `%00`
  elsewhere decodes to a NUL that radosgw keeps: in a query argument, which
  `RGWHTTPArgs::parse` decodes through `url_decode` (`rgw_common.cc:848-898`
  and `:1701-1734` at v19.2.6, `:861-911` and `:1764-1797` at v20.2.4); in
  the copy source, which `parse_copy_location` and, for a part copy,
  `RGWPutObj::init_processing` decode (`rgw_op.cc:5329-5374` and
  `:3807-3851` at v19.2.6, `:5895-5940` and `:4016-4060` at v20.2.4); and
  in an admin argument, which `RESTArgs::get_string` decodes a second time
  (`rgw_rest.cc:854-871` at v19.2.6, `:859-876` at v20.2.4). radosgw then
  names an object, an index object or an attr with the NUL in it, which
  librados's C++ API takes whole (`IoCtx::operate`,
  `include/rados/librados.hpp:1170` at v19.2.6, `:1191` at v20.2.4), so it
  finds no such object, version, upload, user or bucket, or creates one
  under that name. go-ceph hands librados's C API every such name as a C
  string, which ends at the NUL (docs/cgo-limitations.md, "go-ceph cuts
  object names at a NUL byte"), so rgw-go would act on another object
  than the one it authorized. Before authentication, which reads the access
  key's index object, rgw-go refuses:
  - an S3 request, once its method is one an S3 op serves, whose
    `versionId` or `uploadId` holds a NUL; any of whose query arguments'
    names does, as an `x-amz-meta-*` one names an attr (`map_qs_metadata`,
    `rgw_rest_s3.cc:2581-2595` at v19.2.6, `:2742-2756` at v20.2.4); or
    whose `x-amz-copy-source` holds one in its tenant, bucket, key or
    `versionId` as either parse reads it. It answers 400 InvalidArgument,
    radosgw's answer for a copy source it cannot parse. radosgw refuses a
    copy source's tenant holding a NUL too, after authentication, with
    InvalidTenantName (`rgw_validate_tenant_name` in `postauth_init`,
    `rgw_rest_s3.cc:4985-4994` at v19.2.6, `:5545-5554` at v20.2.4).
  - an admin request, once routed, whose `uid`, `tenant`, `email`,
    `access-key`, `subuser`, `account-id`, `bucket`, `bucket-id`,
    `new-bucket-name`, `object`, `id`, `name`, `realm_id` or `period_id`
    holds a NUL as `get_string` decodes it, with 400 InvalidArgument, the
    answer an admin op gives a malformed argument (`-EINVAL`, as from
    `RESTArgs::get_bool`, `rgw_rest.cc:1002-1032` at v19.2.6). radosgw
    reads, creates, links or removes the user, key, subuser, bucket,
    object, account, realm or period under the name with its NUL.

  The credentials are refused as radosgw refuses a name nothing holds, but
  without the lookup: an access key holding a NUL, which only a query's
  `AWSAccessKeyId` or `X-Amz-Credential` can carry, is 403
  InvalidAccessKeyId (`LocalEngine::authenticate`,
  `rgw_rest_s3.cc:6325-6329` at v19.2.6, `:6896-6900` at v20.2.4), and a
  system user's `rgwx-uid` holding one is 403 AccessDenied
  (`SysReqApplier::load_acct_info`, `rgw_auth_filters.h:282-317` at
  v19.2.6, `:304-345` at v20.2.4). A key or user created under such a name
  through radosgw's admin API authenticates on radosgw and not on rgw-go,
  whose admin API refuses to create one. A listing's `prefix`, `delimiter`
  and markers, and every other argument, keep radosgw's handling: they
  reach the bucket index only inside an encoded class call, which carries
  the NUL. The metadata API's `key`, whose NUL radosgw keeps as it keeps
  one in the arguments above, decoding it once (`frame_metadata_key`,
  `rgw_rest_metadata.cc:35-52` at v19.2.6 and v20.2.4), is the metadata
  sections' to check, and they answer a NUL in it with the same 400
  InvalidArgument, after authentication ("The metadata sections write only
  what keeps a user or bucket whole"). A NUL reaches no other name:
  net/http refuses a raw one in the request target or in any header with 400 (`net/url/url.go:436-437`,
  `net/http/server.go:1077-1085` at go1.27.1), and Go's XML decoder refuses
  one in a body, as expat does. A JSON body, such as a bucket policy or an
  admin quota, can carry one as `\u0000` but names no RADOS object, and the
  metadata API's PUT bodies are the metadata sections' to check ("The
  metadata sections write only what keeps a user or bucket whole").

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
  - rgw-go's router gives the same answer, before any handler, to a path
    net/http does not check, which rgw-go otherwise routes as radosgw
    does. One is the path `get_abs_path` takes from an absolute-form
    target whose first `/` after the authority lies in the query, as in
    `GET http://h?x=/b/%zz` (`rgw_common.cc:213-245` at v19.2.6,
    `:213-255` at v20.2.4). The other is a target of another scheme,
    which radosgw keeps whole and net/http parses as opaque, such as
    `foo:%zz`.
  - Routing such a path as radosgw does would serve the request on a path
    its signature does not cover. With a virtual-hosted bucket,
    `url_decode` empties the whole URI at a bad hex digit, bucket
    included. SigV4 signs that bucket in the Host header, and the path's
    empty decode as `/`, the bucket's root (`get_v4_canonical_uri`,
    `rgw_auth_s3.h:598-612` at v19.2.6, `:601-615` at v20.2.4). So a
    signature for ListObjects on the bucket would be served as
    ListBuckets (`docs/ceph-upstream-bugs.md`, same entry).
  - A truncated escape, which `url_decode` and SigV4 cut at the same
    place, is refused too, so that every malformed escape in a path gets
    net/http's answer whatever the target's form.
  - The router refuses a malformed escape in the bucket a Host names too,
    as in `Host: my%zz.<domain>` or `Host: my%.<domain>`: net/http lets a
    `%` through in a Host header, and `preprocess` decodes that bucket
    together with the path (`rgw_rest.cc:2154-2161` and `:2182` at
    v19.2.6, `:2171-2178` and `:2204` at v20.2.4). radosgw serves
    `GET /k` on the first as a ListBuckets, and `GET /` on the second as a
    ListObjects on bucket `my`, another bucket than the Host names.
- **Expectations other than 100-continue.** net/http answers `417
  Expectation Failed`, with `Connection: close`, no body and before any
  handler runs, a request whose `Expect` holds anything but a
  `100-continue`, such as the `Expect: 200` s3-tests sends
  (`server.go:2104-2116`, `sendExpectationFailed` at `:2260-2276`). RFC
  9110 §10.1.1 allows it: a server that receives an Expect field value
  containing a member other than 100-continue MAY respond with 417.
  radosgw ignores such a header and serves the request: beast reads
  `Expect` only to note a `100-continue` (`rgw_asio_frontend.cc:291` at
  both tags), and radosgw sends `100 Continue` only when
  `rgw_print_continue`, true by default, is set and the whole value is
  `100-continue`, compared ASCII case-insensitively
  (`rgw_rest.cc:2268-2269` at v19.2.6, `:2290-2291` at v20.2.4); rgw-go
  does not read `rgw_print_continue`. rgw-go keeps the 417: net/http has
  no hook between reading a request and this check, so serving the
  request would take rewriting each request on the connection before
  net/http reads it, a second HTTP parser in front of net/http's whose
  framing could disagree with it, which is how requests are smuggled. net/http finds `100-continue` as any token of a list
  (`request.go:1536-1538`, `hasToken` at `header.go:240-270`), so a
  value such as `100-continue, 200` gets its `100 Continue` once the
  handler reads the body, where radosgw sends none and serves the request
  once the client sends the body unasked. At the s3-tests commit
  `hack/s3tests/run.sh` pins, `test_bucket_create_bad_expect_mismatch`
  and `test_object_create_bad_expect_mismatch` accept either a success or
  a 417, so both gateways pass them.
- **Shutdown drains.** On SIGTERM rgw-go stops accepting and lets the
  requests in flight finish for up to 20 s, before it closes every
  connection and ends the requests still running. radosgw closes every
  connection at once (`AsioFrontend::stop`, `:1226-1245` at v19.2.6);
  Tentacle waits for the requests in flight first only when
  `rgw_graceful_stop` is set, and it defaults to false (`:1139-1171` at
  v20.2.4). rgw-go reads neither `rgw_graceful_stop` nor
  `rgw_exit_timeout_secs`.
- **The usage log's last flush is bounded.** With `rgw_enable_usage_log`
  set, rgw-go's usage-log worker makes its last flush once the frontend has
  drained, as radosgw finalizes its usage logger only after it stops its
  frontends (`rgw_appmain.cc:589` and `:593` at v19.2.6, `:624` and `:628`
  at v20.2.4), so the requests the drain lets finish are logged. That flush
  gives up 10 s after the drain ends, as does a tick flush still writing
  then, and their entries are lost; radosgw's `~UsageLogger` waits for its
  flush without bound (`rgw_log.cc:128-133` at both tags). The 20 s drain
  and the 10 s flush together fit the 30 s termination grace period a
  radosgw pod gets under Rook, which sets none and so leaves Kubernetes'
  default (rook v1.20.7 sets `TerminationGracePeriodSeconds` only for the
  exporter, `pkg/operator/ceph/cluster/nodedaemon/exporter.go:142`). Where
  the operator gives the pod a shorter grace period than the drain and the
  flush need, a drain that reaches its limit is SIGKILLed before the flush,
  and the usage logged since the last tick is lost.
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
- **Header lines go out sorted by name.** net/http writes a handler's
  header lines sorted by name, byte by byte, and then the ones it adds
  itself, such as Date (`net/http/header.go:167-179` and `:195-205`,
  `net/http/server.go:1562-1564`). radosgw writes the lines an op dumps in
  the order it calls `dump_header`: its frontend holds them until the
  header is complete and then writes them in that order
  (`ReorderingFilter`, `rgw_client_io_filters.h:373-451`, wired at
  `rgw_asio_frontend.cc:320-324`, at v19.2.6 and v20.2.4). The values
  under one name keep their order in both, and clients do not depend on
  the order of lines with different names, which RFC 9110, section 5.3,
  makes insignificant.

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
  (`rgw_log.cc:151` and `:116` at both tags);
  `rgw_override_bucket_index_max_shards` at each bucket creation
  (`init_default_bucket_layout`, `driver/rados/rgw_bucket.cc:2790-2796` at
  v19.2.6, `:2908-2910` at v20.2.4); and `rgw_mp_lock_max_time` and
  `rgw_multipart_min_part_size` at each CompleteMultipartUpload
  (`rgw_op.cc:6430-6431` and `driver/rados/rgw_sal_rados.cc:3458` at
  v19.2.6, `rgw_op.cc:7244-7245` and `driver/rados/rgw_sal_rados.cc:4306`
  at v20.2.4). The same holds for the compressor options its writes read,
  `compressor_zlib_level`, `compressor_zlib_winsize` and
  `compressor_zstd_level`, which radosgw's compressors read at each
  compress
  ([ZlibCompressor.cc:94-100](https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zlib/ZlibCompressor.cc#L94-L100),
  [ZlibCompressor.cc:106-112](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zlib/ZlibCompressor.cc#L106-L112);
  [ZstdCompressor.h:31](https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zstd/ZstdCompressor.h#L31),
  [ZstdCompressor.h:31](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zstd/ZstdCompressor.h#L31)).
  It holds too for `rgw_lc_max_objs` and `rgw_lc_lock_max_time`, which
  radosgw's lifecycle configuration put and delete read each time they
  write a bucket's entry (`get_lc_index` and `guard_lc_modify`,
  [rgw_lc.cc:1966-1976](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1966-L1976)
  and [rgw_lc.cc:2599](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2599),
  [rgw_lc.cc:2023-2033](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2023-L2033)
  and [rgw_lc.cc:2666](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2666)),
  while its workers keep the shard names `rgw_lc_max_objs` gave at startup
  ([rgw_lc.cc:237-248](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L237-L248),
  [rgw_lc.cc:237-248](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L237-L248)),
  an option its own description says not to change while the lifecycle
  index holds entries. It holds as well for the lifecycle worker's options,
  which radosgw reads at each use: `rgw_lifecycle_work_time` at each
  start and each schedule ([rgw_lc.cc:2516](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2516) and [rgw_lc.cc:2556](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2556),
  [rgw_lc.cc:2583](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2583) and [rgw_lc.cc:2623](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2623)); `rgw_lc_debug_interval` at
  each start and each schedule too ([rgw_lc.cc:2529](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2529) and [rgw_lc.cc:2545-2546](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2545-L2546),
  [rgw_lc.cc:2596](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2596) and [rgw_lc.cc:2612-2613](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2612-L2613)), at each bucket's `thread_stop_at`
  ([rgw_lc.cc:2047-2048](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2047-L2048), [rgw_lc.cc:2110-2111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2110-L2111)), and at each session, rollover
  and day check of a pass
  ([rgw_lc.cc:2031-2032](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2031-L2032), [rgw_lc.cc:2143](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2143) and [rgw_lc.cc:2156-2157](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2156-L2157),
  [rgw_lc.cc:2094-2095](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2094-L2095), [rgw_lc.cc:2207](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2207) and [rgw_lc.cc:2220-2221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2220-L2221));
  `rgwlc_auto_session_clear` at each session check ([rgw_lc.cc:2027](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2027),
  [rgw_lc.cc:2090](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2090)); `rgw_lc_lock_max_time` at each pass
  ([rgw_lc.cc:1999](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1999), [rgw_lc.cc:2056](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2056)); `rgwlc_skip_bucket_step` at each
  bucket ([rgw_lc.cc:1693](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1693), [rgw_lc.cc:1749](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1749)); and `rgw_lc_thread_delay`
  at each page of a bucket's listing ([rgw_lc.cc:367](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L367) and [rgw_lc.cc:893](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L893),
  [rgw_lc.cc:367](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L367) and [rgw_lc.cc:914](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L914)). radosgw reads `rgw_lc_max_worker`
  and `rgw_lc_max_wp_worker` once, as it starts its workers
  ([rgw_lc.cc:2469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2469) and [rgw_lc.cc:877](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L877), [rgw_lc.cc:2536](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2536) and
  [rgw_lc.cc:898](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L898)), and so does rgw-go.
- **Shard counts, the bucket-index AIO limit and the copy concurrency that
  are not positive.** A
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
  than the window. With an `rgw_obj_stripe_size` above the window, rgw-go
  refuses an UploadPart once it has read one byte past the window, while
  radosgw reads the whole first stripe before its throttle refuses it
  (`set_head_chunk_size`, `driver/rados/rgw_putobj_processor.cc:477` at
  v19.2.6, `:511` at v20.2.4): a body whose read fails between those two
  points gets the read's own error from radosgw (`rgw_op.cc:4401-4404` at
  v19.2.6, `:4633-4636` at v20.2.4) and 500 UnknownError from rgw-go. The
  default sizes, a 4 MiB stripe and a 16 MiB window, cannot meet this.
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

- **Every document is XML, and every website error page HTML.** radosgw's
  S3 handler chooses its formatter
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
  `:2204-2208`, `:2312-2326` and `:681-686` at v20.2.4). Its website
  endpoint chooses the same way, from HTML by default
  ([rgw_rest_s3.cc:5290-5294](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5290-L5294),
  [rgw_rest_s3.cc:5820-5824](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5820-L5824)), so `format=xml` or
  `Accept: application/xml` turns its error pages into XML documents.
  rgw-go renders every S3 document and error document as XML, those
  included, and every error it answers a website request with once the
  request is parsed as the HTML page radosgw's website endpoint writes by
  default (`htmlErrorPage`, `internal/s3/html.go`), whatever `format` and
  `Accept` say.
- **A website error page ends at its Content-Length.** radosgw writes a
  website request's error page through a pretty HTMLFormatter
  ([rgw_rest.cc:1807-1808](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1807-L1808),
  [rgw_rest.cc:1806-1807](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1806-L1807)) and sends the page's length before
  the flush, which ends a pretty formatter's output with one more line
  break, so a GET's page runs one LF past its Content-Length
  (`docs/ceph-upstream-bugs.md`, "[radosgw sends a website error page one
  byte past its
  Content-Length](ceph-upstream-bugs.md#radosgw-sends-a-website-error-page-one-byte-past-its-content-length)").
  rgw-go sends the page alone, the bytes its Content-Length counts
  (`writeHTMLError`, `internal/s3/html.go`).
- **GetBucketEncryption writes BucketKeyEnabled as true.** radosgw writes
  a bucket key that is on as `True`, which botocore, and so boto3 and the
  AWS CLI, reads as false (docs/ceph-upstream-bugs.md, "[radosgw writes
  GetBucketEncryption's BucketKeyEnabled as True, which botocore reads as
  false](ceph-upstream-bugs.md#radosgw-writes-getbucketencryptions-bucketkeyenabled-as-true-which-botocore-reads-as-false)").
  rgw-go writes `true`, as the AWS SDKs write it
  (`sse.BucketEncryptionConfig.MarshalS3XML`, `internal/sse/xml.go`); every
  other byte of the document is radosgw's. An SDK that reads either case,
  as the AWS SDK for Go does, sees the same configuration from both
  gateways.
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
- **A 204 or 304 carries no Content-Length, whatever
  rgw_print_prohibited_content_length says.** radosgw's frontend completes
  a response whose `end_header` named no length with the length of its
  buffered body (`BufferingFilter::complete_request`,
  `rgw_client_io_filters.h:221-253`), but the filter beneath it drops that
  length from a 204 or a 304 unless `rgw_print_prohibited_content_length`
  is true (`ConLenControllingFilter`, `:323-365`, wired innermost at
  `rgw_asio_frontend.cc:320-324`; the option, default false,
  `common/options/rgw.yaml.in:1056-1062` at v19.2.6, `:1115-1121` at
  v20.2.4; all at both tags). net/http never sends a length with either
  status (`net/http/server.go:1372-1382`), as RFC 9110, section 8.6,
  requires, so rgw-go matches radosgw's default and ignores the option set
  to true.
- **A GetPublicAccessBlock error is its error document alone.** radosgw's
  GetPublicAccessBlock writes the block document whatever its op returned
  (`RGWGetBucketPublicAccessBlock_ObjStore_S3::send_response`,
  [rgw_rest_s3.cc:4613-4624](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4613-L4624),
  [rgw_rest_s3.cc:5124-5135](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5124-L5135)).
  On a bucket without a block, `end_header` has already sent the 404
  NoSuchPublicAccessBlockConfiguration error document under that
  document's Content-Length
  ([rgw_rest.cc:620-625](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L620-L625),
  [rgw_rest.cc:625-630](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L625-L630)),
  so a block with every setting false follows it past that length, and no
  frontend filter cuts it (docs/ceph-upstream-bugs.md, "[radosgw sends a
  public access block after GetPublicAccessBlock's error document, past its
  Content-Length](ceph-upstream-bugs.md#radosgw-sends-a-public-access-block-after-getpublicaccessblocks-error-document-past-its-content-length)").
  A block follows the 500 UnknownError too, which radosgw answers for a
  block that does not decode to every requester `verify_permission`
  admits, the owner included, as its authorizer takes such a block for none
  (`get_public_access_conf_from_attr`,
  [rgw_op.cc:341-355](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L341-L355),
  [rgw_op.cc:371-385](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L371-L385)).
  That block holds the settings decoded before the throw, all four stored
  ones when `DECODE_FINISH` is what throws (`PublicAccessBlockConfiguration::decode`,
  [rgw_public_access.h:54-61](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_public_access.h#L54-L61),
  [rgw_public_access.h:54-61](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_public_access.h#L54-L61)).
  In rgw-go only an admin reaches that 500 ("A public-access block that
  does not decode refuses the request", below).
  rgw-go sends the error document alone, so the response ends at its
  Content-Length (`getPublicAccessBlock`, `internal/s3/publicaccess.go`).
  A client reading by the length sees the same error from both gateways;
  on radosgw it also finds the block left on the connection.
- **A client that went away gets nothing more.** radosgw notices such a
  client only when a read or write on its socket fails: an op that reads
  no more of the request runs to its end, and the response it then writes
  fails, is logged and ends the connection (`dump_status`,
  `rgw_rest.cc:289-301` at v19.2.6 and v20.2.4; `rgw_asio_frontend.cc:350-355`
  at v19.2.6, `:360-365` at v20.2.4). net/http cancels a request's context
  as soon as a read on its connection fails, which it takes for a dead
  connection (`net/http/server.go:803-821` at go1.27.1), and rgw-go's store
  calls stop with that context. When a request fails with that
  cancellation, the S3 and admin handlers write nothing more, log it at
  debug and end the connection. Three things differ:
  - **The op's effect.** An op the cancellation stops leaves its effect
    undone, where radosgw runs it to its end: a DeleteObject whose client
    leaves while its head is still being read removes nothing, as that read
    runs under the request's context, though once its removal starts it runs
    to its end (`readHead` and `removeHead`, `internal/driver/delete.go`).
    PutObject is not one: once its body is read, its write and its second
    quota check go on under a context detached from the request's
    (`internal/driver/put.go`), and the multipart writes detach the same way.
    Nor is a DeleteBucket past its removal's claim: from the entry point's
    read through the owner's list entry it runs to its end, and only its
    last step, the lifecycle entry's removal, stops with the request,
    leaving an entry for a bucket that is gone ("A bucket delete removes
    the lifecycle entry once the bucket is gone"; `DeleteBucket`,
    `internal/driver/bucketops.go`).
    A CompleteMultipartUpload or AbortMultipartUpload stopped while its
    `RGWCompleteMultipart` lock request is in flight, which may still land,
    sends the unlock under the cookie of that one attempt (`lockMeta`,
    `internal/driver/mp_cleanup.go`), so it releases that attempt's hold if
    the lock landed and nothing else: another request of the same gateway
    holding the lock does so under a cookie of its own ("Lock names are
    radosgw's; cookies are not shared"). radosgw waits for the lock's reply
    and unlocks only a hold it saw (`MPRadosSerializer::try_lock`,
    `rgw_sal_rados.cc:3750-3760` at v19.2.6, `:4613-4624` at v20.2.4;
    `RGWCompleteMultipart::complete`, `rgw_op.cc:6582-6593`, `:7506-7517`).
    The unlock is sent too when the lock never left the RADOS client's
    in-flight limit (`internal/radosclient/goceph/limiter.go`), and it waits
    there with no deadline, so while a PG is unavailable and its ops, which
    `rados_osd_op_timeout` at its default of 0 never times out, fill that
    limit, such a request keeps its `rgw_max_concurrent_requests` slot,
    which S3 and admin requests share, until the unlock is admitted and
    answered. radosgw blocks the same way: its lock waits in the objecter
    throttle holding its scheduler slot and is never cancelled. On either
    gateway a long PG outage can so fill the cap and answer every further
    request 503 SlowDown.
  - **The usage log and the metrics.** They file the request under the 408
    RequestTimeout a context error maps to when nothing was sent yet, so the
    usage entry counts no successful op, and under the status sent
    otherwise. radosgw files the status of its op's own result.
  - **A client that half-closed its connection and still reads** gets no
    answer, where radosgw would answer its op.
- **A bucket of another zonegroup is redirected as radosgw redirects it,
  and never removed.** An S3 request on a bucket whose instance names a
  zonegroup `RGWZoneGroup::equals` does not take for this one answers 301
  PermanentRedirect with no body once the bucket loads, before the
  permission check, as `rgw_build_bucket_policies` answers it
  (`rgw_op.cc:558-574` at v19.2.6, `:588-604` at v20.2.4): a missing
  zonegroup id is the master zonegroup's, and GetBucketLocation, a system
  request to the master zonegroup and a PUT of an object whose
  `x-amz-copy-source` bucket this zonegroup holds are let through. The
  `Location` header is built from this zonegroup's endpoint, or its master
  zone's, and the request URI and query as sent, and is left out when
  neither has an endpoint (`abort_early`, `rgw_rest.cc:716-727` at
  v19.2.6, `:721-732` at v20.2.4). Such a bucket arises from instance
  metadata carried over from another cluster with `radosgw-admin metadata
  put`, which radosgw and rgw-go both accept whatever zonegroup it names.
  rgw-go differs in four ways:
  - An S3 route rgw-go does not serve answers 501 NotImplemented before
    the bucket is loaded, so such a bucket is never redirected there;
    radosgw loads the bucket for every op but CreateBucket and answers 301.
  - The RADOS store's bucket removal refuses a bucket of another
    zonegroup with 301 PermanentRedirect before it reads the index or
    removes anything, and a purge refuses it before it deletes an object.
    radosgw's S3 delete reaches such a bucket only as a system request to
    the master zonegroup, which the redirect lets through (`rgw_op.cc:565`
    at v19.2.6, `:595` at v20.2.4): the delete the owning zonegroup's zone
    forwards to the metadata master (`rgw_forward_request_to_master`,
    `rgw_op.cc:126-134` at v19.2.6, `:161-169` at v20.2.4) once its own
    emptiness check has passed. The master compares the raw ids, skips the
    check (`rgw_op.cc:3759-3783` at v19.2.6, `:3968-3992` at v20.2.4) and
    removes the bucket without listing it
    (`driver/rados/rgw_sal_rados.cc:366-369` at v19.2.6, `:383-386` at
    v20.2.4). rgw-go neither forwards a delete nor receives a forwarded one
    (Multisite), so no zone has checked such a bucket, and removing it would
    leave its objects in the data pool with nothing naming them. The
    refusal is what a system request's S3 DeleteBucket on the master
    zonegroup meets, and what `DELETE /admin/user` with `purge-data`
    answers for a user owning such a bucket, keeping the user, that bucket
    and the buckets it had not yet reached. radosgw's user removal removes
    the bucket without listing it and leaves its objects
    (`driver/rados/rgw_user.cc:1977` at v19.2.6, `:1983` at v20.2.4).
    `DELETE /admin/bucket` answers 301 earlier, as radosgw does.
  - A copy between zonegroups runs here. A copy into a bucket of another
    zonegroup from a source this zonegroup holds, which the redirect lets
    through, writes the object into that bucket's index here, and a copy
    from a source bucket of another zonegroup into a bucket of this one
    reads the source here. radosgw's `copy_obj` sends the first through
    `copy_obj_to_remote_dest` to the metadata master, the master zone of
    the period's master zonegroup (`get_master_conn`,
    `driver/rados/rgw_rados.cc:4615` and `services/svc_zone.cc:547-550` at
    v19.2.6, `:4864` and `:383-385` at v20.2.4), which is in the
    destination's zonegroup only when that is the master zonegroup. It
    fetches the second with `fetch_remote_obj` through the source
    zonegroup's connection, answering 404 NoSuchKey when it has none
    (`driver/rados/rgw_rados.cc:4716-4736`, `:4808-4810` and `:4270-4278`
    at v19.2.6, `:4966-4990`, `:5064-5066` and `:4473-4481` at v20.2.4).
  - A bucket whose instance records no zonegroup is the master
    zonegroup's to both gateways, and S3 serves it there. Its removal
    syncs the owner's stats, refuses it while it holds an object with 409
    BucketNotEmpty and aborts its uploads, and `DELETE /admin/user` with
    `purge-data` deletes its objects before it removes it, as for any
    bucket of this zonegroup. radosgw compares the raw ids in both, takes
    the bucket for another zonegroup's, and removes it without checking or
    listing it, leaving its objects in the data pool with nothing naming
    them (`rgw_op.cc:3759-3772` at v19.2.6, `:3968-3981` at v20.2.4;
    `driver/rados/rgw_user.cc:1977` at v19.2.6, `:1983` at v20.2.4;
    docs/ceph-upstream-bugs.md, "radosgw's bucket delete and user purge
    orphan the objects of a bucket with no zonegroup id").
- **CORS rules are matched as Amazon S3 matches them.** rgw-go answers a
  CORS request from the first rule, in stored order, that allows its
  Origin, its method and, for a preflight, every header its
  Access-Control-Request-Headers names (`cors.Configuration.Match`,
  `internal/cors/match.go`). Amazon S3 "uses the first `CORSRule` rule that
  matches the incoming browser request" on those three
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/cors.html#cors-eval-criteria;
  the PutBucketCors reference as the AWS SDK for Go carries it,
  [api.go:8329-8342](https://github.com/aws/aws-sdk-go/blob/7112c0a0c2d01713a9db2d57f0e5722225baf5b5/service/s3/api.go#L8329-L8342)),
  and so does radosgw's `match_rule` on main since cc1dcecee08, on the
  tentacle branch since 5f25f2bb0e2 and on the squid branch since
  50137a456e2, which no release carries, though for every request rather
  than a preflight alone. Both floors
  take the first rule whose AllowedOrigin matches the Origin alone. They
  refuse a preflight 403 when that rule does not allow its method or a
  requested header, and send no CORS headers on another response when it
  does not allow the method, so a later rule for the same origin is never
  used (docs/ceph-upstream-bugs.md, "[radosgw answers a CORS request with
  the first rule whose origin
  matches](ceph-upstream-bugs.md#radosgw-answers-a-cors-request-with-the-first-rule-whose-origin-matches)").
  rgw-go also differs from the floors in what a rule allows and in how much
  a preflight may ask:
  - **An empty Origin matches no rule**, as main's `RGWCORSRule::matches`
    refuses one. The floors match an `Origin` header with an empty value
    against a rule that allows `*`, so an anonymous request carrying one is
    sent `Access-Control-Allow-Origin: *` (`generate_cors_headers`,
    [rgw_op.cc:1541-1600](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1541-L1600),
    [rgw_op.cc:1778-1837](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1778-L1837)).
  - **A wildcard is matched as written.** An AllowedOrigin or AllowedHeader
    with one `*` allows a value that starts with what precedes the `*` and
    ends with what follows it, the two not overlapping, spaces and tabs
    compared as they are. That is the plain reading of Amazon S3's rule
    that an origin "can contain only one `*` wildcard character, such as
    `http://*.example.com`", and an AllowedHeader "at most one", with
    `x-amz-*` as its example
    (https://docs.aws.amazon.com/AmazonS3/latest/userguide/ManageCorsUsing.html);
    AWS documents no answer for an overlap or for whitespace. A member with
    more than one `*`, which PutBucketCors refuses but a stored rule may
    hold, allows only itself. radosgw lets the prefix and the suffix
    overlap and drops spaces and tabs from the pattern, which allows values
    the pattern does not cover, and reads a pattern with more than one `*`
    through its first one or two pieces, which allows less than the
    pattern covers (docs/ceph-upstream-bugs.md, "[radosgw's CORS wildcard
    match allows origins and headers its pattern does not
    cover](ceph-upstream-bugs.md#radosgws-cors-wildcard-match-allows-origins-and-headers-its-pattern-does-not-cover)").
  - **A preflight naming more than 32 distinct headers matches no rule.**
    `Match` compares the requested header names in ASCII lower case, each
    once, and refuses a request naming more than `cors.MaxRequestHeaders`,
    32, as it refuses one that no rule allows; `ResponseHeaders` then
    allows none of them. radosgw has no limit. One rule costs its one-`*`
    AllowedHeaders times the distinct names asked for, and an
    unauthenticated preflight chooses both the bucket and the names. The
    densest configuration a 1 MiB PutBucketCors body stores holds about
    30,700 one-`*` members; against 32 names such as `X-Amz-Meta-07`, sent
    once or repeated in other cases to 64 KiB, one preflight took 4.2 ms of
    CPU on the development host. A larger stored configuration, which a
    radosgw with a raised `rgw_max_put_param_size` or a direct RADOS write
    can leave, costs proportionally more: the densest 1 MiB attr, about
    149,600 members, took 20.7 ms. Without a limit, 64 KiB of distinct
    names take seconds. A browser names only the headers its page sets
    beyond the CORS-safelisted ones.

  None of this changes the stored configuration, which both gateways read
  and write alike.
- **A CORS expose header's control bytes are sent escaped.** radosgw from
  v17.2.0 on writes a line feed in a stored ExposeHeader as `\n` in
  Access-Control-Expose-Headers, and a carriage return, which a
  PutBucketCors body carries as `&#13;`, as it is. CVE-2021-3524's fix,
  which also writes the carriage return as `\r`, reached only v14.2.21,
  v15.2.12, v16.2.4 and the later releases of those three series
  (docs/ceph-upstream-bugs.md, "[radosgw from Quincy on lacks
  CVE-2021-3524's fix for CORS expose
  headers](ceph-upstream-bugs.md#radosgw-from-quincy-on-lacks-cve-2021-3524s-fix-for-cors-expose-headers)").
  rgw-go writes the carriage return as `\r`, byte for byte what v16.2.4's
  `format_exp_headers` writes
  ([rgw_cors.cc:145-155](https://github.com/ceph/ceph/blob/v16.2.4/src/rgw/rgw_cors.cc#L145-L155)).
  It also writes every other byte below 0x20 but a tab, and 0x7f, as `\x`
  and two lower-case hex digits (`cors.Rule.ResponseHeaders`,
  `internal/cors/match.go`). radosgw sends those as they are, and ends the
  value at a NUL, because `dump_access_control` takes it as a C string
  ([rgw_rest.cc:526-554](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L526-L554),
  [rgw_rest.cc:531-559](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L531-L559)).
  Of those bytes a PutBucketCors body can carry only DEL, as `&#127;`; the
  others reach a stored rule only when something else writes it. The
  stored configuration keeps every byte, and GetBucketCors renders it as
  radosgw does.
- **DeleteBucketCors removes a CORS configuration that does not decode.**
  radosgw's DeleteBucketCors decodes the bucket's `user.rgw.cors` before
  it removes it, and answers one that does not decode with
  `read_bucket_cors`'s -EIO, 500 UnknownError, leaving it in place
  ([rgw_op.cc:6133-6137](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6133-L6137) at v19.2.7,
  [rgw_op.cc:6800-6804](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6800-L6804) at v20.2.4;
  `docs/ceph-upstream-bugs.md`, "[radosgw's DeleteBucketCors refuses, and
  keeps, a CORS configuration that does not
  decode](ceph-upstream-bugs.md#radosgws-deletebucketcors-refuses-and-keeps-a-cors-configuration-that-does-not-decode)").
  rgw-go's op removes the attr whatever it holds and succeeds
  (`op.DeleteBucketCORS`, `internal/op/cors.go`), which its route, once the
  S3 handler routes the CORS ops, will answer 204, as Amazon S3's
  DeleteBucketCors, which has no such refusal, answers and as radosgw
  answers a bucket with a configuration it can read. A bucket with no attr
  succeeds with nothing written in both. The state left is the absent attr
  radosgw's own delete writes, which both gateways read as no
  configuration.
- **The CORS configuration ops check the storage class once the requester
  is authorized.** radosgw reads `x-amz-storage-class` for every S3 request
  ([rgw_rest_s3.cc:5073-5076](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5073-L5076) at v19.2.7,
  [rgw_rest_s3.cc:5603-5606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5603-L5606) at v20.2.4)
  and, while it loads the bucket, before any permission check, refuses one
  the bucket's placement lacks with 400 InvalidArgument
  ([rgw_op.cc:576-583](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L576-L583) at v19.2.7,
  [rgw_op.cc:606-613](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L606-L613) at v20.2.4),
  so a requester the bucket refuses learns from GetBucketCors, PutBucketCors
  or DeleteBucketCors whether its placement has that class. rgw-go's ops
  make the check once the requester is authorized, before they read the
  configuration or the body, as PutObject and CopyObject make it ("PutObject
  and CopyObject authorize a request before they read what the store holds
  for it", below), so once the S3 handler routes them a refused requester
  will get 403 AccessDenied. Three refusals will come ahead of the 400 that
  radosgw's bucket load answers first: a suspended bucket's, an unparsable
  policy's, and the 500 rgw-go's authorizer answers every requester, an
  admin included, for a bucket ACL that does not decode
  (`internal/authz/evaluator.go:327-330`). The preflight, which authorizes
  nobody, makes the check where radosgw does, after the redirect and ahead
  of an ACL that does not decode, and its answer is the same for every key
  (`op.GetBucketCORS`,
  `op.PutBucketCORS`, `op.DeleteBucketCORS` and `op.OptionsCORS`,
  `internal/op/cors.go`).
- **Every S3 response carries CORS headers once its bucket is loaded.**
  radosgw's `end_header` adds the headers of the bucket's CORS configuration
  only when it is given the op
  ([rgw_rest.cc:603-605](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L603-L605) at v19.2.7,
  [rgw_rest.cc:608-610](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L608-L610) at v20.2.4),
  and on both releases these answer without it:
  - GetBucketCors, PutBucketCors, DeleteBucketCors,
    PutBucketRequestPayment, GetBucketMetaSearch, PutObjectLockConfiguration,
    PutObjectRetention, PutObjectLegalHold, PutPublicAccessBlock,
    DeletePublicAccessBlock, PutBucketPolicy, DeleteBucketPolicy,
    PutBucketNotification and DeleteBucketNotification, and DeleteObjects'
    refusal before its first key, which radosgw answers with its status line
    alone (docs/ceph-upstream-bugs.md, "[radosgw answers fifteen more S3
    operations without CORS
    headers](ceph-upstream-bugs.md#radosgw-answers-fifteen-more-s3-operations-without-cors-headers)");
  - PutBucketEncryption and DeleteBucketEncryption
    (docs/ceph-upstream-bugs.md, "[radosgw's PutBucketEncryption and
    DeleteBucketEncryption answer without CORS
    headers](ceph-upstream-bugs.md#radosgws-putbucketencryption-and-deletebucketencryption-answer-without-cors-headers)");
  - ListBuckets and CreateBucket, for which no bucket's configuration is
    loaded.

  radosgw leaves them out only of what each one's `send_response` answers:
  the success, and the refusals `execute` makes, PutBucketEncryption's 411,
  416 and 400 MalformedXML among them. A refusal made once the bucket
  has loaded and before `execute`, by the op mask, the permission check or
  the rate limiter, goes through `abort_early`, which passes the op
  ([rgw_rest.cc:746](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L746) at v19.2.7,
  [rgw_rest.cc:746](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L746) at v20.2.4),
  and carries them.

  rgw-go adds them to every S3 response once the request's bucket record is
  loaded, error documents and DeleteObjects' status line included
  (`SetCommonHeaders`, `internal/s3/headers.go`): one rule for every route,
  as Amazon S3 evaluates a bucket's CORS configuration for each request,
  with no route table to keep per release. A request with no bucket record
  gets none, as on radosgw: ListBuckets, CreateBucket, whose record holds
  none of the bucket's attrs, and a refusal before the bucket loads, of the
  authentication or of a bucket that does not exist. The headers are
  derived as radosgw's `generate_cors_headers` derives them
  ([rgw_op.cc:1541-1600](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1541-L1600) at v19.2.7,
  [rgw_op.cc:1778-1837](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1778-L1837) at v20.2.4),
  from the rule that "CORS rules are matched as Amazon S3 matches them"
  (above) chooses for the request:
  - the method is Access-Control-Request-Method when the request sends one,
    and its own otherwise, on any request, as both floors and main take it.
    Amazon S3's API reference matches a request other than a preflight by
    its own method: "The request method (for example, GET, PUT, HEAD, and so
    on) or the Access-Control-Request-Method header in case of a pre-flight
    OPTIONS request must be one of the AllowedMethod elements"
    (https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketCors.html).
    rgw-go departs from that, and "CORS rules are matched as Amazon S3
    matches them" does not cover this case. It keeps radosgw's reading: s3-tests'
    `test_cors_origin_response` asserts it, answering a PUT that sends
    Access-Control-Request-Method GET with GET's headers, and a browser never
    sends that header on a request other than a preflight, as Fetch makes it
    a forbidden request header
    (https://fetch.spec.whatwg.org/#forbidden-request-header);
  - Access-Control-Request-Headers chooses no rule on a request other than a
    preflight, and the rule allows those of its names it covers, none when
    they name more than 32 distinct headers (`cors.MaxRequestHeaders`), as a
    preflight naming more matches no rule (above). radosgw allows every name
    the rule covers, however many. The limit is the preflight's: it bounds
    the CPU the names cost against the rule's one-`*` AllowedHeaders, and an
    ordinary request, an anonymous one included, chooses both the bucket and
    the names. Main's `generate_cors_headers` chooses the rule with them for
    every request
    ([rgw_op.cc:2003-2010](https://github.com/ceph/ceph/blob/cc1dcecee08/src/rgw/rgw_op.cc#L2003-L2010) at cc1dcecee08);
  - Access-Control-Allow-Origin is `*` for a rule that allows every origin
    when the request carries no Authorization header, a presigned request
    among them, as radosgw sends it;
  - the configuration is the one the request's bucket record holds as the
    response is written, so once the S3 handler routes the CORS ops a
    PutBucketCors will be answered from the configuration it stored and a
    DeleteBucketCors from none;
  - a configuration that does not decode adds none, and the response is the
    op's. radosgw's `generate_cors_headers` stores its read's -EIO as the
    op's result once the status is decided, which leaves the listings and
    GetBucketEncryption a 200 with no document (docs/ceph-upstream-bugs.md,
    "[radosgw sends no document to a request with an Origin when the
    bucket's CORS configuration does not
    decode](ceph-upstream-bugs.md#radosgw-sends-no-document-to-a-request-with-an-origin-when-the-buckets-cors-configuration-does-not-decode)");
  - an attr `rgw_extended_http_attrs` maps to a CORS header's name, `Vary`
    among them, goes out ahead of that header on a GetObject, as radosgw
    sends both (`endHeaderNames`, `internal/s3/getobject.go`).

  A preflight's response will carry only its own Access-Control headers
  once the S3 handler routes the preflight, which it answers 501 until then
  ("A CORS preflight is answered 501 until CORS lands", below): those of the
  rule, Origin, method and requested headers its op found, the Origin as
  sent, as radosgw's preflight sends them (`SetPreflightHeaders`,
  `internal/s3/headers.go`; `RGWOptionsCORS_ObjStore_S3::send_response`,
  [rgw_rest_s3.cc:3854-3875](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3854-L3875) at v19.2.7,
  [rgw_rest_s3.cc:4305-4326](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4305-L4326) at v20.2.4).
  `SetCommonHeaders` adds none to an OPTIONS response, and no
  x-amz-request-charged ("A preflight's answer says nothing of its key's
  user", below). radosgw's preflight sends none with the refusals its
  `execute` makes, a 400 or a 403, and neither will rgw-go's. A preflight
  refused once its bucket has loaded goes through `abort_early` on radosgw,
  which sends it the headers `generate_cors_headers` derives for any op,
  `*` or the Origin by the Authorization header:
  - the bucket load's 301 PermanentRedirect for a bucket of another
    zonegroup, 400 InvalidArgument for a storage class its placement lacks
    and 500 for an ACL that does not decode;
  - `init_processing`'s refusal when the user or account of the quota owner
    the bucket's ACL names does not load
    ([rgw_op.cc:1411-1434](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1411-L1434) at v19.2.7,
    [rgw_op.cc:1648-1671](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1648-L1671) at v20.2.4);
  - the op mask's and OPA's refusals
    ([rgw_process.cc:201-219](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L201-L219) at v19.2.7,
    [rgw_process.cc:201-219](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L201-L219) at v20.2.4),
    and the rate limiter's
    ([rgw_process.cc:247-250](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L247-L250) at v19.2.7,
    [rgw_process.cc:250-253](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L250-L253) at v20.2.4).

  rgw-go's preflight will make the bucket load's refusals alone, with none
  of those headers. It asks for no op mask (`op.OptionsCORS.OpMask` is 0),
  loads no quota owner and calls no OPA, and phase 2 has no rate limiter,
  so a preflight the others refuse on radosgw will be answered from the
  configuration ("A preflight's answer says nothing of its key's user",
  below). A browser fails a preflight answered outside 200-299 whatever its
  headers (https://fetch.spec.whatwg.org/#cors-preflight-fetch), and the
  preflight's headers will come from its op's results alone, never from the
  Authorization header.
- **A POST form part ends only at a delimiter line.** radosgw's POST form
  reader takes `--` and the boundary anywhere for a part's end, with the
  CRLF before it when there is one
  ([rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1173-L1220) at v19.2.7,
  [rgw_rest.cc:1173-1220](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1173-L1220) at v20.2.4):
  in a part's data, so a file or field whose data holds that text mid-line
  is cut there, and in a part's header lines, which `read_line` reads with
  the same `index_of`, so a header holding it, a filename for one, is cut
  there and ends the part's headers
  ([rgw_rest.cc:1294-1300](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1294-L1300) at v19.2.7,
  [rgw_rest.cc:1294-1300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1294-L1300) at v20.2.4;
  [rgw_rest.cc:1359-1361](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1359-L1361) at v19.2.7,
  [rgw_rest.cc:1359-1361](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1359-L1361) at v20.2.4)
  (docs/ceph-upstream-bugs.md, "[radosgw cuts a POST upload's file at its
  boundary's text
  mid-line](ceph-upstream-bugs.md#radosgw-cuts-a-post-uploads-file-at-its-boundarys-text-mid-line)").
  rgw-go ends a part only at a delimiter line, as RFC 2046 section 5.1.1
  defines it for multipart/form-data (RFC 7578): `--` and the boundary at
  the start of a line, which the body, a part's data and every CRLF start,
  then, for the close delimiter, `--`, then transport padding, spaces and
  tabs, which RFC 2046 says "receivers MUST be able to handle", then CRLF,
  or for the close delimiter the body's end (`delimiterEnd`,
  `internal/s3/postform.go`). It takes at most 256 bytes of padding
  (`formMaxPadding`), so a delimiter is decided within its buffer. A line
  that goes on from the boundary to padding or `--` and does not complete a
  delimiter, with more padding than that, other text after the padding or
  after the close's `--`, or a close ended by a bare LF, is 400
  InvalidArgument, "Malformed request", and with no message in a part after
  the file, wherever it falls: RFC 2046 keeps the boundary delimiter out of
  a part, so no conforming composer sends one, and Amazon S3 answers a body
  that is not well-formed multipart/form-data MalformedPOSTRequest. A
  boundary followed by other text, such as `--Bxyz`, is data. radosgw
  closes the form at any line that starts with `--`, the boundary and `--`,
  whatever follows
  ([rgw_rest.cc:1279-1283](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1279-L1283) at v19.2.7,
  [rgw_rest.cc:1279-1283](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1279-L1283) at v20.2.4):
  as the form's last line it stores the file as sent, and inside the file's
  data it stores the file cut there. So a client that ends its form with a
  bare-LF close, which RFC 2046's CRLF forbids, is refused here where
  radosgw stores its file. A header line is read whole, to its CRLF. radosgw ends the
  part at a padded delimiter too, but reads the padding and its CRLF as the
  next part's empty header block, so that part loses its name
  (docs/ceph-upstream-bugs.md, "[radosgw reads the part after a padded POST
  form delimiter without its
  headers](ceph-upstream-bugs.md#radosgw-reads-the-part-after-a-padded-post-form-delimiter-without-its-headers)").
  rgw-go skips a preamble before the first delimiter, which radosgw reads
  as the first part's header lines, refusing a line without a colon and
  misfiling the first part after one with a colon
  ([rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1311-L1370) at v19.2.7,
  [rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1311-L1370) at v20.2.4)
  (docs/ceph-upstream-bugs.md, "[radosgw parses a POST form's preamble as
  its first part's header
  lines](ceph-upstream-bugs.md#radosgw-parses-a-post-forms-preamble-as-its-first-parts-header-lines)").
  A part delimiter among a part's header lines, which radosgw skips once at
  the part's start and otherwise takes for the end of the headers, is 400
  InvalidArgument, "Malformed request". A close delimiter there ends the
  form in both, and after the file part, where radosgw's
  `complete_get_params` then answers 400 for want of the part's data
  ([rgw_rest_s3.cc:3256-3278](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3256-L3278) at v19.2.7,
  [rgw_rest_s3.cc:3392-3414](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3392-L3414) at v20.2.4),
  rgw-go reads it as the form's end.
- **A POST form that ends before its file part's delimiter is refused.**
  radosgw's part loop ends with no error when a field's data ends at the
  close delimiter
  ([rgw_rest_s3.cc:2923-2971](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2923-L2971) at v19.2.7,
  [rgw_rest_s3.cc:3049-3097](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3049-L3097) at v20.2.4),
  so a form with no file part goes on to store the object from what
  follows the close delimiter; and `get_data` reads a file part that no
  delimiter ends up to the body's end, which the write stores
  ([rgw_rest_s3.cc:3280-3303](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3280-L3303) at v19.2.7,
  [rgw_rest_s3.cc:3416-3439](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3416-L3439) at v20.2.4;
  [rgw_op.cc:4695-4721](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4695-L4721) at v19.2.7,
  [rgw_op.cc:5008-5035](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5008-L5035) at v20.2.4)
  (docs/ceph-upstream-bugs.md, "[radosgw stores a POST form with no file
  part, or a file part no delimiter
  ends](ceph-upstream-bugs.md#radosgw-stores-a-post-form-with-no-file-part-or-a-file-part-no-delimiter-ends)").
  Amazon S3's error list answers the first IncorrectNumberOfFilesInPostRequest,
  "POST requires exactly one file upload per request.", and a body that is
  not well-formed multipart/form-data MalformedPOSTRequest, "The body of
  your POST request is not well-formed multipart/form-data.", both 400
  (aws-sdk-go's copy of the list,
  [api.go:20085-20087](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20085-L20087) and
  [api.go:20213-20215](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20213-L20215) at v1.55.8).
  rgw-go refuses both with 400 InvalidArgument, as its error set is
  radosgw's table, which has neither code (`internal/op/errors.go:50`): a
  form that closes before its file part, wherever it closes, with "Malformed
  request", as radosgw answers one that closes where a part's headers
  start; and a body that ends before a delimiter it needs, in the preamble,
  a part's headers, a field or the file, with "Couldn't find boundary",
  radosgw's answer to a field whose delimiter it cannot find
  (`formReader.Fields` and `formFile.Read`, `internal/s3/postform.go`).
- **A POST form field's name holds no control byte.** radosgw keeps a
  part's `name` parameter whole: `index_of` ends a header line only at a
  CRLF or the boundary, `read_form_part_header` takes the name from the
  line's `name` parameter
  ([rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1311-L1370) at v19.2.7,
  [rgw_rest.cc:1311-1370](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1311-L1370) at v20.2.4),
  and `get_params` files the part under it
  ([rgw_rest_s3.cc:2968-2970](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2968-L2970) at v19.2.7,
  [rgw_rest_s3.cc:3094-3096](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3094-L3096) at v20.2.4),
  a NUL, a CR, an LF or any other control byte included. Its part map
  compares names as C strings, so a name holding a NUL is the name before
  the NUL to every lookup, which Tentacle's checksum exception reads past
  (docs/ceph-upstream-bugs.md, "[Tentacle's POST policy excepts a field
  whose name holds a NUL after a checksum
  algorithm's](ceph-upstream-bugs.md#tentacles-post-policy-excepts-a-field-whose-name-holds-a-nul-after-a-checksum-algorithms)").
  rgw-go answers a part before the file whose name holds a byte below 0x20
  or DEL 400 InvalidArgument, "A form field name holds a control
  character" (`formReader.Fields`, `internal/s3/postform.go`): kept whole,
  an `x-amz-meta-` name would reach an xattr name, which go-ceph hands
  librados as a C string (`WriteOp.SetXattr`), and a GET's response header.
  A part's header names, Content-Disposition's among them, are compared
  over all their bytes, where radosgw's `stringcasecmp` ends them at a NUL.
  Parts after the file, which neither gateway keeps, are not checked.
- **A POST form's fields before its file are bounded.** radosgw reads every
  part before the file into memory, each up to `rgw_max_chunk_size` and
  with no bound on their number, before it compares the request's
  Content-Length with `rgw_max_put_size` and before it authenticates the
  form
  ([rgw_op.cc:4576-4590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4576-L4590) at v19.2.7,
  [rgw_op.cc:4869-4883](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4869-L4883) at v20.2.4),
  and its frontend puts no limit on a body
  ([rgw_asio_frontend.cc:248-258](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L248-L258) at v19.2.7,
  [rgw_asio_frontend.cc:249-259](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L249-L259) at v20.2.4)
  (docs/ceph-upstream-bugs.md, "[radosgw holds every POST form field in
  memory before it authenticates the
  form](ceph-upstream-bugs.md#radosgw-holds-every-post-form-field-in-memory-before-it-authenticates-the-form)").
  rgw-go lets the parts before the file's data, the preamble and their
  header lines included, take at most `rgw_max_chunk_size` and 1 MiB of the
  body, room for one field of radosgw's largest beside the rest of a form,
  and answers a form past that 400 InvalidArgument with the description
  Amazon S3 gives its MaxPostPreDataLengthExceededError, "Your POST request
  fields preceding the upload file were too large." (`formFieldsAllowance`
  and `errFormTooLarge`, `internal/s3/postform.go`). Amazon S3 publishes no
  number for its own bound, so this one is rgw-go's choice. rgw-go answers a
  part header line that, with its CRLF, is longer than the window radosgw
  finds a line end in, `rgw_max_chunk_size`, 2 and the boundary's length
  ([rgw_rest.cc:1195-1200](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1195-L1200) at v19.2.7,
  [rgw_rest.cc:1195-1200](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1195-L1200) at v20.2.4;
  [rgw_rest.cc:1222-1254](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1222-L1254) at v19.2.7,
  [rgw_rest.cc:1222-1254](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1222-L1254) at v20.2.4),
  400 InvalidArgument with no message, as radosgw answers a header line it
  cannot parse. radosgw's window is narrower when input is left over from
  the last read, since it reads more only when `rgw_max_chunk_size` passes
  what it holds
  ([rgw_rest.cc:1230](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1230) at v19.2.7,
  [rgw_rest.cc:1230](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1230) at v20.2.4), so rgw-go's
  bound is its widest; radosgw cuts a longer line at the window and reads
  the rest as the next header line (docs/ceph-upstream-bugs.md, "[radosgw
  cuts a POST form header line longer than its read window and reads the
  rest as another header
  line](ceph-upstream-bugs.md#radosgw-cuts-a-post-form-header-line-longer-than-its-read-window-and-reads-the-rest-as-another-header-line)").
- **A quoted POST form parameter keeps its semicolons.** radosgw splits the
  parameters of the request's Content-Type and of a part's
  Content-Disposition at every `;`
  ([rgw_rest.cc:1112-1145](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1112-L1145) at v19.2.7,
  [rgw_rest.cc:1112-1145](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1112-L1145) at v20.2.4),
  so `filename="my;cat.jpg"` gives the filename `"my`, which a key's
  `${filename}` then takes (docs/ceph-upstream-bugs.md, "[radosgw cuts a
  POST form's quoted parameter at a
  semicolon](ceph-upstream-bugs.md#radosgw-cuts-a-post-forms-quoted-parameter-at-a-semicolon)").
  In rgw-go a parameter whose value opens with a double quote runs to the
  closing quote, a `;` inside it included, as RFC 2045's quoted-string
  holds one (section 5.1) (`parseFormParams`, `internal/s3/postform.go`).
  It reads the parameters otherwise as radosgw does: names compared
  exactly, a later one replacing an earlier, and a value unquoted only when
  both its ends are quotes; the Content-Type's type is compared exactly
  too, as radosgw compares it.

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
- **A chunked body is read whatever the case of its Transfer-Encoding.**
  radosgw reads a body sent without a Content-Length only when
  `HTTP_TRANSFER_ENCODING` is exactly `chunked` (`strcmp`,
  `rgw_rest.cc:1567-1569` at v19.2.6, `:1572-1574` at v20.2.4), while beast
  decodes the chunked framing in any case and files the value as sent. So
  for `Transfer-Encoding: Chunked` radosgw answers PutBucketAcl,
  PutBucketPolicy, PutBucketTagging, PutObjectAcl and PutObjectTagging with
  411 MissingContentLength, and so PutObject, which makes the same
  comparison in its `get_params` (`rgw_rest_s3.cc:2599-2604` at v19.2.6,
  `:2760-2765` at v20.2.4), and CreateBucket leaves the body unread and
  creates the bucket as if none was sent. rgw-go reads the body in every
  case, through the payload verifier and after the permission check, since
  net/http accepts the header in any case and removes it ("A chunked request's Transfer-Encoding reads as
  `chunked`", above), so the case the client sent cannot be seen.
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
- **A CORS preflight is answered 501 until CORS lands.** radosgw serves an
  OPTIONS request on a bucket or an object as its CORS preflight op
  (`op_options`,
  [rgw_rest_s3.cc:4823-4826](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4823-L4826) and
  [rgw_rest_s3.cc:4908-4911](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4908-L4911) at v19.2.7,
  [rgw_rest_s3.cc:5348-5351](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5348-L5351) and
  [rgw_rest_s3.cc:5438-5441](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5438-L5441) at v20.2.4).
  rgw-go authenticates a preflight as radosgw does, a signed one without
  comparing its signature (`docs/ceph-upstream-bugs.md`, "radosgw grants a
  signed CORS preflight without comparing its signature"), but serves no
  preflight yet: where radosgw runs the preflight, for a signed or an
  anonymous OPTIONS request, rgw-go answers 501 NotImplemented.
- **A preflight's answer says nothing of its key's user.** radosgw grants
  a signed preflight without comparing its signature, so its key's id is
  all its sender has to show (`docs/ceph-upstream-bugs.md`, "radosgw grants
  a signed CORS preflight without comparing its signature"). Two of
  radosgw's answers that come before any preflight op runs then tell that
  sender more than any forged request of another method does, which radosgw
  and rgw-go answer 403 SignatureDoesNotMatch for every key that resolves:
  - a suspended user's preflight is 403 UserSuspended, as the suspended
    check runs for every op
    ([rgw_process.cc:372-374](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L372-L374) at v19.2.7,
    [rgw_process.cc:374-376](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L374-L376) at v20.2.4);
  - a system user's preflight takes its `rgwx-uid`: the preflight's grant
    goes through `create_apl_local`
    ([rgw_rest_s3.cc:6397](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6397) at v19.2.7,
    [rgw_rest_s3.cc:6927](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6927) at v20.2.4),
    which wraps the local applier in `SysReqApplier`
    ([rgw_auth_s3.h:186](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.h#L186) at v19.2.7,
    [rgw_auth_s3.h:188](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L188) at v20.2.4),
    and its `load_acct_info` answers 403 AccessDenied when the user or
    account `rgwx-uid` names does not load
    ([rgw_auth_filters.h:283-317](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_filters.h#L283-L317) at v19.2.7,
    [rgw_auth_filters.h:305-345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_filters.h#L305-L345) at v20.2.4).
    That tells the sender that the key is a system user's and whether the
    user or account it names exists (`docs/ceph-upstream-bugs.md`,
    "radosgw's signed CORS preflight takes a system key's rgwx-uid
    unverified").

  radosgw's other such answers come after the grant, each from the key's
  user:
  - the bucket load reads the request's `rgwx-bucket-instance` before
    anything else, and only a system key's request can name one:
    `SysReqApplier` makes the `rgwx-` query parameters visible for a system
    user alone
    ([rgw_auth_filters.h:320-331](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_filters.h#L320-L331) and
    [rgw_common.h:438-443](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L438-L443) at v19.2.7,
    [rgw_auth_filters.h:348-359](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_filters.h#L348-L359) and
    [rgw_common.h:462-467](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.h#L462-L467) at v20.2.4).
    A value without a colon is 400 InvalidArgument
    ([rgw_op.cc:498-505](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L498-L505) and
    [rgw_common.cc:3156-3161](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L3156-L3161) at v19.2.7,
    [rgw_op.cc:528-535](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L528-L535) and
    [rgw_common.cc:3212-3217](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L3212-L3217) at v20.2.4),
    so a preflight of any bucket, on a single zone, that names one is 400
    for a system key and the CORS answer for any other;
  - the bucket load lets a system key's preflight through where it refuses
    another key's: to a bucket of another zonegroup when this zonegroup is
    the master, and to a suspended bucket or one whose policy does not
    parse, which on Tentacle an admin's key passes too
    ([rgw_op.cc:366-370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L366-L370),
    [rgw_op.cc:565](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L565) and
    [rgw_op.cc:612-614](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L612-L614) at v19.2.7,
    [rgw_op.cc:396-400](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L396-L400),
    [rgw_op.cc:595](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L595) and
    [rgw_op.cc:642-644](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L642-L644) at v20.2.4);
  - `init_quota` runs for the preflight before the op mask check and loads
    the user or account of the owner the bucket's ACL names
    ([rgw_op.cc:552](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L552) at v19.2.7,
    [rgw_op.cc:582](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L582) at v20.2.4),
    except for a system key and a key
    whose op mask lacks both the write and the delete bit, and fails the
    request when that load fails
    ([rgw_op.h:253-261](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L253-L261) and
    [rgw_op.cc:1411-1434](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1411-L1434) at v19.2.7,
    [rgw_op.h:266-274](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L266-L274) and
    [rgw_op.cc:1648-1671](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1648-L1671) at v20.2.4),
    so on a bucket whose ACL names an owner that does not load those keys
    are answered and every other key, and an anonymous preflight, refused;
  - the op mask check refuses a user whose op mask lacks the read bit
    ([rgw_op.h:1763](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L1763) and
    [rgw_op.cc:1017-1035](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1017-L1035) at v19.2.7,
    [rgw_op.h:1919](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1919) and
    [rgw_op.cc:1216-1234](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1216-L1234) at v20.2.4);
  - the rate limiter answers 503 SlowDown once that user's limit is spent,
    and exempts a system user's key, and on Squid an admin's too
    ([rgw_process.cc:96-98](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L96-L98) at v19.2.7,
    [rgw_process.cc:97](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L97) at v20.2.4);
  - the preflight's x-amz-request-charged says whether that user owns a
    requester-pays bucket
    ([rgw_rest.cc:597-601](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L597-L601) at v19.2.7,
    [rgw_rest.cc:602-606](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L602-L606) at v20.2.4).

  The same registry entry has each. rgw-go has no rate limiter yet; when it
  has one, a preflight is limited as an anonymous request.

  rgw-go answers every preflight whose key resolves alike, on both
  releases. It authenticates a preflight so today: it runs a suspended
  user's preflight (`internal/s3/handler.go:421`), takes no `rgwx-uid` on
  any preflight (`internal/auth/verifier.go:132-137`), and reads
  `rgwx-bucket-instance` on no request. A suspended user's own preflight
  therefore passes where radosgw refuses it; the request it precedes is
  still refused. Only radosgw's own multisite connection sends `rgwx-`
  parameters, and multisite is excluded, so no client rgw-go serves loses
  one. Every preflight is still answered 501 ("A CORS preflight is answered
  501 until CORS lands", above); once the S3 handler routes it to its op,
  `op.OptionsCORS` (`internal/op/cors.go`), the answer will depend on no
  key:
  - the op asks for no op mask, so the key of a user without the read bit
    will get the CORS answer where radosgw answers 403 AccessDenied;
  - it loads the bucket without a system exemption, so another zonegroup's
    bucket will be 301 PermanentRedirect for every preflight;
  - of the bucket load's refusals, it makes those whose answer radosgw
    gives every key alike, and so answers a bucket whose ACL does not
    decode with radosgw's 500 UnknownError for every key
    ([rgw_op.cc:232-238](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L232-L238) and
    [rgw_op.cc:548-550](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L548-L550) at v19.2.7,
    [rgw_op.cc:275-281](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L275-L281) and
    [rgw_op.cc:578-580](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L578-L580) at v20.2.4);
  - it makes none of those whose answer radosgw gives by key kind, which
    rgw-go makes in its authorizer, and the preflight checks no permission:
    while the bucket's ACL decodes, a suspended bucket and a bucket whose
    policy does not parse will be answered from the CORS configuration, for
    every key and for an anonymous preflight alike, where radosgw answers
    all but a system key, and on Tentacle an admin's, 403 UserSuspended or
    403 AccessDenied. When the ACL does not decode as well, every preflight
    of such a bucket will be 500, where radosgw answers the anonymous
    preflight and every key but those 403: a suspended bucket's refusal
    returns before the ACL is decoded, and a policy's -EACCES replaces the
    decode's error
    ([rgw_op.cc:366-370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L366-L370) and
    [rgw_op.cc:612-614](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L612-L614) at v19.2.7,
    [rgw_op.cc:396-400](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L396-L400) and
    [rgw_op.cc:642-644](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L642-L644) at v20.2.4),
    and only those keys get its 500;
  - it loads no owner for a quota, so a bucket whose ACL names an owner
    that does not load will be answered from its configuration for
    everyone, where radosgw refuses an anonymous preflight and every key
    but a system key and one without the modify bits;
  - the request a preflight precedes is still refused in each of these;
  - a preflight's answer will carry no x-amz-request-charged, which
    `SetCommonHeaders` (`internal/s3/headers.go`) adds to no OPTIONS
    response, anonymous or signed, so it will say nothing of who owns a
    requester-pays bucket;
  - its usage is accounted as an anonymous request's (the next entry).

  The one thing of the key's user a preflight's answer will depend on is
  its tenant. A bucket name that names none resolves under the key user's
  tenant, as radosgw resolves it
  ([rgw_rest_s3.cc:4995-4998](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4995-L4998) and
  [rgw_bucket.cc:127-130](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_bucket.cc#L127-L130) at v19.2.7,
  [rgw_rest_s3.cc:5525-5528](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5525-L5528) and
  [rgw_bucket.cc:127-130](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_bucket.cc#L127-L130) at v20.2.4),
  which is radosgw's reason for looking the key up at all (ceph commit
  fe15b52edb5): a tenanted user's page names its bucket without the tenant,
  and its preflight must find that bucket. A key whose user is in tenant
  `t` will therefore get, for the name `b`, the answer an anonymous
  preflight of `t:b` gets, so the answers of two keys whose users differ
  only in tenant will differ only where anonymous preflights of the name
  under each tenant do, and that is no departure from radosgw. What it
  tells a sender holding only a key's id is which tenant's bucket of that
  name answers, from which the sender can test guesses at the key user's
  tenant against anonymous preflights of `t:b`; resolving under the default
  tenant instead would refuse every tenanted user's preflight of a bucket
  named that way.
- **A preflight's x-amz-mfa is not checked.** radosgw grants a signed CORS
  preflight on its key's id alone (`docs/ceph-upstream-bugs.md`, "[radosgw
  grants a signed CORS preflight without comparing its
  signature](ceph-upstream-bugs.md#radosgw-grants-a-signed-cors-preflight-without-comparing-its-signature)")
  and runs `postauth_init` for it as for any request
  ([rgw_process.cc:365](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L365),
  [rgw_process.cc:367](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L367)),
  which checks its `x-amz-mfa` against the key user's devices
  ([rgw_rest_s3.cc:5026-5029](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5026-L5029),
  [rgw_rest_s3.cc:5556-5559](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5556-L5559)).
  Each such check counts against the device, which takes five in a step
  and then refuses every code until they age out, and a good code checked
  is used up (`docs/ceph-upstream-bugs.md`, "[radosgw checks a signed CORS
  preflight's x-amz-mfa against its key's user's
  device](ceph-upstream-bugs.md#radosgw-checks-a-signed-cors-preflights-x-amz-mfa-against-its-keys-users-device)").
  rgw-go checks no `x-amz-mfa` for an identity granted on its key's id
  alone, on both releases (`verifyMFA`, `internal/s3/request.go`). The
  preflight reads no MFA verdict, so its answer is the same; what differs
  is the device's record, on which a forged preflight records no check.
- **A preflight's usage is accounted as an anonymous request's.** radosgw
  files a preflight's usage as any request's (`log_usage`,
  [rgw_log.cc:195-251](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_log.cc#L195-L251) at v19.2.7,
  [rgw_log.cc:195-251](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_log.cc#L195-L251) at v20.2.4).
  With a bucket named, the record is the bucket owner's (`:207-208`), and the
  request's user is its payer on a requester-pays bucket unless the answer is
  403 (`:215-219`); with no bucket named, the record is `s->owner`'s
  (`:203`), which `Strategy::apply` takes from the identity
  ([rgw_auth.cc:534](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth.cc#L534) at v19.2.7,
  [rgw_auth.cc:549](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L549) at v20.2.4).
  A system user's request is not logged at all (`:197-198`). So forged
  preflights, which need only a key's id, inflate the bucket owner's usage,
  the key user's as payer, or, with no bucket named, the key user's or its
  account's: a bucket token that ends at its colon, `OPTIONS /t:` or
  `/t:/k`, names none and is answered 400 InvalidBucketName.

  rgw-go's `op.LogUsage` filed a preflight as radosgw does. It now accounts
  an identity the verifier marks as granted on its key's id alone as an
  anonymous request's (`internal/op/usagelog.go:32-42`), on both releases:
  - with a bucket named, the record stays the bucket owner's, as an
    anonymous preflight's is, and the payer on a requester-pays bucket is
    the anonymous user, not the key's user;
  - with no bucket named, the record is the anonymous user's, not the key's
    user's or account's;
  - a system user's preflight is logged as an anonymous one, not left out.

  The preflight is answered 501 until CORS lands, and that answer loads no
  bucket, so today only the bucketless case writes an entry.
- **A SigV2 preflight is signed over the method it asks for on Squid too.**
  Tentacle builds a SigV2 OPTIONS request's string to sign over its
  `Access-Control-Request-Method`, and answers 400 InvalidArgument when that
  header is missing or names no CORS method, once the credentials,
  Content-MD5 and the request date have passed and before the skew check
  and the key lookup
  (`get_canonical_method`, called at
  [rgw_auth_s3.cc:258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L258),
  [rgw_auth_s3.cc:1749-1776](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L1749-L1776) at v20.2.4).
  Squid applies that rule to SigV4 alone (`get_v4_canonical_method`,
  [rgw_auth_s3.cc:705-732](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L705-L732),
  called at
  [rgw_rest_s3.cc:5868](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5868) at v19.2.7)
  and signs a SigV2 one over its own method, `info.method`
  ([rgw_auth_s3.cc:256](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L256) at v19.2.7).
  rgw-go takes Tentacle's rule on both releases, as it takes Tentacle's
  gateway-side request parsing. So on Squid a SigV2 preflight without the
  header, or naming another method, is 400 InvalidArgument from rgw-go
  before the skew check and the key lookup. Squid's radosgw goes on: a
  skewed Date is 403 RequestTimeTooSkewed and an unknown key 403
  InvalidAccessKeyId, and a known key's preflight, whatever its signature,
  runs the preflight op (`RGWOptionsCORS::execute`,
  [rgw_op.cc:6177-6207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6177-L6207) at v19.2.7).
  That op answers a missing header with 400 InvalidArgument too
  (`:6189-6194`), but only once the bucket has loaded, a missing one being
  404 NoSuchBucket first (`:539-540`), and the request carries an Origin, a
  missing one being 400 InvalidArgument first (`:6183-6188`). It answers a
  header naming another method, `get` or PATCH among them, with 403
  AccessDenied, as no CORS rule allows it and the op sends that -ENOENT as
  -EACCES
  ([rgw_rest_s3.cc:3861-3862](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3861-L3862) at v19.2.7).
  Neither gateway compares a preflight's signature, so the method it is
  signed over changes nothing else.
- **A system request's rgwx-uid names its owner, not its user record.**
  Only radosgw's own multisite connection sends a system request with
  `rgwx-uid` or `rgwx-perm-check-uid` (`RGWRESTConn`,
  `rgw_rest_conn.h:234-237` and `rgw_rest_conn.cc:330-331` at v20.2.4),
  and rgw-go answers a SigV4 one on both floors as Squid does; it reads no
  SigV2 request's `rgwx-uid` ("A SigV2 request's rgwx-uid is not read",
  below). With a non-empty
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
- **A body read whole is checked against its signed hash.** radosgw reads
  the body of CreateBucket, the ACL, policy and tagging PUTs,
  CompleteMultipartUpload, DeleteObjects and the other subresource PUTs
  through `read_all_input`, which drops the verdict of the payload's
  SHA-256 check (`rgw_op.h:215-237` at v19.2.6, `:229-251` at v20.2.4;
  `docs/ceph-upstream-bugs.md`, "[radosgw ignores a payload-hash mismatch
  on bodies read by
  read_all_input](ceph-upstream-bugs.md#radosgw-ignores-a-payload-hash-mismatch-on-bodies-read-by-read_all_input)"),
  so it acts on a signed body whose hash does not match
  `x-amz-content-sha256`. rgw-go reads such a body to its end through the
  authenticator's verifying reader before it parses or acts on it, and
  refuses one whose hash does not match with 400 XAmzContentSHA256Mismatch,
  changing nothing.
- **SigV4 accepts an unsigned Content-Type and x-amz-content-sha256 on
  Tentacle too.** Unless `rgw_sigv4_insecure` is set, v20.2.4 refuses with
  403 AccessDenied a request that carries a Content-Type or an
  `x-amz-content-sha256` header its SignedHeaders do not list, as it
  refuses an unsigned `host` or any other unsigned `x-amz-*` header
  ([rgw_auth_s3.cc:765-797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L765-L797)). v19.2.7 requires neither to
  be signed and keeps the rest
  ([rgw_auth_s3.cc:788-842](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L788-L842)); the tentacle branch carries
  both fixes, which no Tentacle tag does yet (`docs/ceph-upstream-bugs.md`,
  "[radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is
  unsigned](ceph-upstream-bugs.md#radosgw-1926-and-2024-reject-a-sigv4-request-whose-content-type-is-unsigned)"
  and "[radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose
  x-amz-content-sha256 is
  unsigned](ceph-upstream-bugs.md#radosgw-1926-and-2024-reject-a-sigv4-request-whose-x-amz-content-sha256-is-unsigned)").
  rgw-go takes v19.2.7's rule on both releases (`canonicalHeadersV4`), so
  on Tentacle it verifies such a request on its signature where radosgw
  refuses it: a presigned URL used with a Content-Type of the client's own,
  as a browser upload sends, and a request whose signer left either header
  out of SignedHeaders. The checks came with the CVE-2026-54330 hardening
  (GHSA-rmjq-ffrm-j6vj), and the relaxation keeps what they close. The
  advisory's escalation is an unsigned `x-amz-*` header that radosgw then
  applies, granting more than the signer intended; every `x-amz-*` header
  but `x-amz-content-sha256` is still refused unsigned, beside an unsigned
  Content-Type or not. `x-amz-content-sha256` is read once as the payload
  hash, which ends the canonical request and is what the body is then held
  to, on the header route, the presigned route and the boto2 fallback alike
  (`expectedPayloadHash`, as radosgw's
  [rgw_auth_s3.h:641-664](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L641-L664)), so any
  value but the one the signer used fails the signature. A Content-Type
  grants no authority, since rgw-go routes nothing on it, but what the
  relaxation does allow is this: on Tentacle, whoever holds a presigned PUT,
  or any request that does not sign Content-Type, chooses the type the
  object is stored under and later served with, `text/html` included,
  which v20.2.4 refuses. v19.2.7 behaves the same; what AWS S3 accepts is
  untested. A signer that wants the type fixed signs it. AWS's SigV4 page,
  https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html
  (read 2026-10-14), asks for "any `x-amz-*` headers" to be signed, so for
  `x-amz-content-sha256` the rule is looser than AWS's letter; it stays safe
  because the value is the canonical request's HashedPayload. On
  Content-Type the page is inconsistent: a present one "must" be added to
  CanonicalHeaders, yet other standard headers "such as content-type" are
  optional. The owner decided on 2026-10-13 that Tentacle takes the rule too
  once that analysis held. s3-tests' `test_sigv4_contenttype_unsigned`,
  `test_sigv4_presigned_contenttype_unsigned` and
  `test_sigv4_without_signed_content_sha256` send such requests, and
  v20.2.4's radosgw fails them (`test/s3tests/baseline/tentacle.json`).
- **The S3 API reads X-Amz- headers only.** radosgw's `init_meta_info`
  files every header under `X-Amz-`, `X-Goog-`, `X-Dho-`, `X-Rgw-`,
  `X-Object-`, `X-Container-` or `X-Account-` into `x_meta_map` under its
  `x-amz-` name, so `X-Goog-Meta-K` is filed as `x-amz-meta-k`, and one
  whose `x-amz-` name starts `x-amz-server-side-en` into
  `crypt_attribute_map` too
  ([rgw_common.cc:413-464](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L464),
  [rgw_common.cc:426-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L426-L477)).
  SigV2's string to sign carries that map
  ([rgw_auth_s3.cc:246](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L246),
  [rgw_auth_s3.cc:248](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L248)),
  and the stored attrs and user metadata, the encryption settings and
  their `s3:x-amz-server-side-encryption*` condition keys, and
  CreateBucket's object lock
  ([rgw_rest_s3.cc:2542](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2542),
  [rgw_rest_s3.cc:2695](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2695))
  are taken from it, while most directives read the raw `HTTP_X_AMZ_`
  header. Its SigV4 check refuses an unsigned header only under
  `HTTP_X_AMZ_`
  ([rgw_auth_s3.cc:819-841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L819-L841),
  [rgw_auth_s3.cc:780-796](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L780-L796)).
  So under SigV4 radosgw stores an unsigned `X-Goog-Meta-*` and lets an
  unsigned `X-Goog-Server-Side-Encryption-*` set the upload's encryption
  (`docs/ceph-upstream-bugs.md`, "[radosgw's SigV4 check misses the
  meta-prefixed headers init_meta_info renames to
  x-amz](ceph-upstream-bugs.md#radosgws-sigv4-check-misses-the-meta-prefixed-headers-init_meta_info-renames-to-x-amz)"),
  and under SigV2 a signed header can be moved between `X-Amz-` and
  another prefix with its signature intact, which drops a signed directive
  or makes one of a header its signer sent under another prefix
  (`docs/ceph-upstream-bugs.md`, "[radosgw's SigV2 signature survives
  moving a signed x-amz directive under another meta
  prefix](ceph-upstream-bugs.md#radosgws-sigv2-signature-survives-moving-a-signed-x-amz-directive-under-another-meta-prefix)").

  rgw-go does not rename, on both releases. On its S3 API a header under
  the six other prefixes is an ordinary header, as on AWS, and rgw-go gives
  it no metadata, attr, signature or directive. The owner decided so on
  2026-10-14: the prefixes serve Google Cloud Storage interoperability,
  DreamHost and Swift's namespace, none of which rgw-go's S3 API offers,
  and the renaming is what makes a signed directive movable. So:
  - SigV2's string to sign names the `x-amz-` headers alone
    (`amzHeadersV2`). A signed header moved between `X-Amz-` and another
    prefix fails with 403 SignatureDoesNotMatch, as on AWS, and so does a
    request whose signer signed a header under another prefix under its
    `x-amz-` name, as radosgw does, while one whose signer left such a
    header out, as botocore's SigV2 signer does, verifies where radosgw
    fails it;
  - SigV4 requires host and every `x-amz-*` header but
    `x-amz-content-sha256` to be signed, as radosgw's check does, and no header under the six (`canonicalHeadersV4`),
    so one may go unsigned as any ordinary header may, rgw-cache's `X-Rgw-Auth`
    on every method among them ("X-Rgw-Auth"). A header SignedHeaders
    lists is signed whatever its name;
  - the stored `x-amz-` attrs and user metadata come from `X-Amz-` headers,
    and on a PutObject or an UploadPart also from the query's `x-amz-meta-*`
    as radosgw's `map_qs_metadata` does, but for SigV2's header route ("A
    SigV2 request's unsigned query meta and encryption settings are not
    read"), never from a header under the six (`headerMeta`,
    `requestAttrs`), so an `X-Goog-Meta-*` header stores nothing and GET and
    HEAD send nothing for it;
  - a write asks for encryption only by an `X-Amz-` header or the query
    ("Writes that ask for encryption answer 501, SSE-S3 and SSE-KMS until
    phase 2 and SSE-C until phase 4"), and the authorizer's encryption
    condition keys read `X-Amz-` alone (`cryptAttr`);
  - CreateBucket's object lock is `X-Amz-Bucket-Object-Lock-Enabled` alone,
    where radosgw honours it under the six as well.

  The cost: a client that relied on radosgw storing `X-Goog-Meta-*`, as a
  client of Google Cloud Storage's XML API sends custom metadata, loses
  that metadata, with no error; a write that names encryption under
  another prefix, which radosgw encrypts or fails, is stored unencrypted,
  as on AWS, where such a header asks for nothing; a CreateBucket that
  asks for object lock under another prefix, which radosgw honours,
  creates a bucket without object lock, with no error, so a client that
  meant to create a locked bucket gets an unlocked one; and a SigV2 signer
  that signs a header under another prefix as radosgw does is refused. A
  client that sends `X-Amz-` headers, as the AWS SDKs do, sees no
  difference. radosgw shares the renaming between its APIs, as
  `RGWREST::get_handler` runs `init_meta_info` for each
  ([rgw_rest.cc:2320](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2320),
  [rgw_rest.cc:2337](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2337)):
  its Swift handlers store container, account and object metadata through
  the same `x_meta_map`, under `x-amz-` names
  ([rgw_op.cc:4855](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4855),
  [rgw_op.cc:4948](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4948)
  and [rgw_op.cc:5041](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5041);
  [rgw_op.cc:5197](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5197),
  [rgw_op.cc:5290](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5290)
  and [rgw_op.cc:5383](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5383)),
  and read container and account metadata by name only to remove it, or,
  for a container's `X-Container-Meta-Access-Control-*`, to build its CORS
  ([rgw_rest_swift.cc:728-731](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_swift.cc#L728-L731),
  [rgw_rest_swift.cc:750-781](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_swift.cc#L750-L781)).
  A Swift API, if rgw-go adds one, maps its own namespace
  (`X-Object-Meta-*`, `X-Container-Meta-*` and `X-Account-Meta-*`)
  into the same stored attributes explicitly, by name, so the S3 API
  keeps reading `X-Amz-` alone.
  s3-tests' `test_sigv2_signed_copy_source_moved_to_xgoog`
  ([jhoblitt/ceph#5](https://github.com/jhoblitt/ceph/pull/5)) resends a
  signed SigV2 CopyObject's `X-Amz-Copy-Source` as `X-Goog-Copy-Source`,
  and `test_sigv4_presigned_put_xgoog_meta_unsigned`, as revised by
  [jhoblitt/ceph#8](https://github.com/jhoblitt/ceph/pull/8), the PR into
  jhoblitt/ceph's `rgw-go-s3tests` branch, sends a presigned PUT with an
  unsigned `X-Goog-Meta-Foo` and passes when no metadata is stored from
  it. Run
  against both releases' clusters on 2026-10-14, rgw-go passed both,
  answering the moved copy source 403 SignatureDoesNotMatch and storing
  the PUT with no metadata, and radosgw failed both, answering the copy 200
  and storing `x-amz-meta-foo` from the unsigned header.
- **A presigned SigV2 request's signed meta and session token mean the same
  in its headers and its query.** The presigned string to sign writes the
  query's `x-amz-meta-*` and `x-amz-security-token` lines straight after the
  headers' `x-amz-*` lines, each set sorted on its own
  ([rgw_auth_s3.cc:164-165](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L164-L165),
  [rgw_auth_s3.cc:165-166](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L165-L166);
  [rgw_auth_s3.cc:175-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L175-L187),
  [rgw_auth_s3.cc:176-188](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L176-L188)),
  so a run of those lines can move between the end of the headers and the
  start of the query with the signature kept, and botocore's presigned URLs
  carry the meta and the token they sign in the query. radosgw gives the two
  forms different meanings (`docs/ceph-upstream-bugs.md`, "[radosgw's
  presigned SigV2 signature survives moving signed meta or a session token
  between the headers and the
  query](ceph-upstream-bugs.md#radosgws-presigned-sigv2-signature-survives-moving-signed-meta-or-a-session-token-between-the-headers-and-the-query)"):
  only a PutObject or an UploadPart adds the query's meta to what it stores
  ([rgw_rest_s3.cc:2619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2619),
  [rgw_rest_s3.cc:2772](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2772)),
  a CopyObject or a CreateMultipartUpload storing the headers' alone; and
  `sts:authentication` is true only for a token in the headers
  ([rgw_op.cc:904-909](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L904-L909),
  [rgw_op.cc:939-944](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L939-L944)).
  rgw-go, on both releases, takes the meta and the token a presigned SigV2
  request signs in its query, once the request has authenticated, as if
  they were sent as `X-Amz-` headers, for every read of the request
  (`queryMetaV2`, `s3.ApplyAuth`):
  - a CopyObject under `REPLACE` and a CreateMultipartUpload store meta
    signed in the query, which radosgw drops; a PutObject and an UploadPart
    store what radosgw stores, and a CopyObject under `COPY` keeps the
    source's meta in either form, as radosgw's does;
  - `sts:authentication` is true for a token signed in the query, where
    radosgw's is false;
  - a name signed on both sides is refused with 403 AccessDenied, where
    radosgw joins a PutObject's two meta values with a comma, stores the
    header's alone on the other writes, and reads the token from the query
    to authenticate and from the header for `sts:authentication`;
  - an empty session token is refused with 403 AccessDenied in the headers
    and under a query name in any case, where radosgw refuses an empty one
    only in the query, under a name that reads `x-amz-security-token` once
    radosgw lower-cases a name holding `X-Amz-`
    ([rgw_common.cc:870-878](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L870-L878),
    [rgw_common.cc:883-891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L883-L891))
    ([rgw_rest_s3.cc:6104-6109](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6104-L6109),
    [rgw_rest_s3.cc:6634-6639](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6634-L6639)).
    An empty `X-AMZ-SECURITY-TOKEN=` or `x-Amz-security-token=` signs the
    same `x-amz-security-token:` line, as the string to sign lower-cases the
    name, and radosgw serves it as a request with no token, with
    `sts:authentication` false.

  The refusals come once the request has authenticated, so a request
  radosgw refuses for its access key, its signature or its expiry gets
  radosgw's error, and one that authenticates is answered 403 where
  radosgw serves it. A client that signs each name on one side, as
  botocore's presigned URLs do, sees no refusal. No engine of rgw-go's
  reads the session token to authenticate a request; one that does, an STS
  engine among them, must take a presigned request's token from this one
  set too, never from its query or its headers alone. The header route,
  whose string to sign carries no query, folds nothing, and its query meta
  is not read at all ("A SigV2 request's unsigned query meta and
  encryption settings are not read"); SigV4, whose canonical query names
  each parameter, is unchanged. s3-tests'
  `test_sigv2_presigned_multipart_meta_moved_to_query` and
  `test_sigv2_presigned_copy_replace_meta_in_query`
  ([jhoblitt/ceph#6](https://github.com/jhoblitt/ceph/pull/6))
  send a CreateMultipartUpload and a `REPLACE`
  CopyObject with their meta signed in the query. Run against both
  releases' clusters on 2026-10-14, rgw-go stored the meta, and radosgw
  answered 200 and stored none of it.
- **A presigned SigV2 request is refused when its query meta or session
  token holds a line break.** `get_canon_amz_hdrs` writes each
  `x-amz-meta-*` and `x-amz-security-token` line of a presigned string to
  sign as `name:value` and a newline, with nothing escaped
  ([rgw_auth_s3.cc:66-86](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L66-L86),
  [rgw_auth_s3.cc:67-87](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L67-L87)),
  and the query's names and values are url-decoded first
  ([rgw_common.cc:865](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L865),
  [rgw_common.cc:878](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L878)),
  so one such parameter whose name or value holds `%0A` signs as the
  several lines it spells. `x-amz-meta-a=1%0Ax-amz-meta-b:2` signs as the
  headers `X-Amz-Meta-A: 1` and `X-Amz-Meta-B: 2` do, and
  `x-amz-meta-a=1%0Ax-amz-security-token:tok` as `X-Amz-Meta-A: 1` beside a
  signed session token, which the packed form hides
  (`docs/ceph-upstream-bugs.md`, "[radosgw's presigned SigV2 string to sign
  takes a line break in a query meta name or value for a line of its
  own](ceph-upstream-bugs.md#radosgws-presigned-sigv2-string-to-sign-takes-a-line-break-in-a-query-meta-name-or-value-for-a-line-of-its-own)").
  radosgw accepts the packed request and stores one attr where the signer
  signed two, or drops the token. rgw-go refuses with 403 AccessDenied, on
  both releases, a presigned SigV2 request with an `x-amz-meta-*` or
  `x-amz-security-token` query parameter whose name or value, url-decoded,
  holds a CR or an LF (`queryMetaV2`), at the same point as the refusals
  above: a request radosgw refuses for its access key, its signature or
  its expiry gets radosgw's error, and one that authenticates is answered
  403 where radosgw serves it. A header value cannot carry either byte to
  rgw-go, as net/http joins a continuation line with a space and answers
  400 to a value holding any other control character, and the header route
  signs no query, so it needs no such refusal. The refusal covers the
  packed form alone: SigV2 cannot tell a signer's packed value from the
  request its holder splits out of it, which carries the same signature. A
  signer who signs a meta value holding a line break, such as
  `1%0Ax-amz-security-token:tok`, has also signed the request that sends
  that token on a line of its own, and rgw-go serves that request, as
  radosgw does, while it refuses the signer's own. s3-tests'
  `test_sigv2_presigned_meta_lines_packed_into_one_query_value`
  ([jhoblitt/ceph#6](https://github.com/jhoblitt/ceph/pull/6))
  packs a botocore-presigned PUT's
  two meta parameters into one. Run against both releases' clusters on
  2026-10-14, rgw-go refused it, and radosgw stored it with 200.
- **A presigned SigV2 request is refused when its Expires holds a line
  break.** The presigned date line is `Expires` as sent, up to a NUL
  ([rgw_auth_s3.cc:217](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L217),
  [rgw_auth_s3.cc:219](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L219)),
  and its expiry is read with `atoll`, which stops at the first byte that
  is no digit
  ([rgw_rest_s3.cc:6090-6101](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6090-L6101),
  [rgw_rest_s3.cc:6620-6631](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6620-L6631)),
  so `Expires=1441000000%0Ax-amz-copy-source:b/src` signs as a request with
  the header `X-Amz-Copy-Source: b/src` and carries none: radosgw serves a
  presigned CopyObject so resent as a PutObject of the request's body
  (`docs/ceph-upstream-bugs.md`, "[radosgw's presigned SigV2 date line
  takes a line break in Expires for a line of its
  own](ceph-upstream-bugs.md#radosgws-presigned-sigv2-date-line-takes-a-line-break-in-expires-for-a-line-of-its-own)").
  rgw-go refuses with 403 AccessDenied, on both releases, a presigned SigV2
  request whose `Expires` holds a CR or an LF (`expiresLineBreak`), once the
  request has authenticated, as the refusals above. An `Expires` is a
  number, so no client sends one. The refusal covers the packed form alone:
  the request with those lines sent as headers carries the same signature
  and is served. s3-tests'
  `test_sigv2_presigned_copy_source_moved_to_expires`
  ([jhoblitt/ceph#6](https://github.com/jhoblitt/ceph/pull/6))
  sends such a CopyObject. Run against both
  releases' clusters on 2026-10-14, rgw-go refused it, and radosgw stored
  it as a PutObject with 200.
- **A presigned SigV2 request is refused when a query meta name holds a
  colon.** A query `x-amz-meta-*` line is its name, `:` and its value
  ([rgw_auth_s3.cc:66-86](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L66-L86),
  [rgw_auth_s3.cc:67-87](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L67-L87)),
  so the name `x-amz-meta-a:x` with the value `y` signs as `x-amz-meta-a`
  with the value `x:y`, and radosgw stores it under the other name
  (`docs/ceph-upstream-bugs.md`, "[radosgw's presigned SigV2 string to sign
  takes a colon in a query meta name for the end of the
  name](ceph-upstream-bugs.md#radosgws-presigned-sigv2-string-to-sign-takes-a-colon-in-a-query-meta-name-for-the-end-of-the-name)").
  rgw-go refuses with 403 AccessDenied, on both releases, a presigned SigV2
  request with a query `x-amz-meta-*` whose name holds a colon
  (`queryMetaV2`), at the same point; a value may hold one. No header name
  can hold a colon, so no client names its meta that way. The refusal
  covers the colon in the name alone: the request with the colon moved
  into the value carries the same signature and is served. s3-tests'
  `test_sigv2_presigned_meta_name_with_colon`
  ([jhoblitt/ceph#6](https://github.com/jhoblitt/ceph/pull/6))
  sends such a PUT. Run against both releases'
  clusters on 2026-10-14, rgw-go refused it, and radosgw stored it with
  200.
- **A SigV2 request is refused when a signed sub-resource's value spells a
  later one.** The canonical resource joins the signed sub-resources with
  `&` in a fixed order, each url-decoded value written as it is
  ([rgw_auth_s3.cc:91-124](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L91-L124),
  [rgw_auth_s3.cc:92-125](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L92-L125)),
  so `response-content-disposition=x%26versionId%3DV` signs as a GET of
  version `V`, which radosgw then serves at its current version
  (`docs/ceph-upstream-bugs.md`, "[radosgw's SigV2 canonical resource takes
  an ampersand in a sub-resource value for the next
  sub-resource](ceph-upstream-bugs.md#radosgws-sigv2-canonical-resource-takes-an-ampersand-in-a-sub-resource-value-for-the-next-sub-resource)").
  rgw-go refuses with 403 AccessDenied, on both releases and both SigV2
  routes, a request whose signed sub-resource's url-decoded value holds
  `&`, the name of a sub-resource the resource writes after it, and `&` or
  the value's end, or `=` for one written with its value
  (`packedSubresourceV2`), at the same point. Any
  other `&` passes, so a `response-content-disposition` naming the file
  `a&b.txt` is served as radosgw serves it; one naming `a&versionId=1` is
  refused. The refusal covers the packed form alone: SigV2 cannot tell a
  signer's packed value from the request its holder splits out of it,
  which carries the same signature. A signer who signs a
  `response-content-disposition` of `x&versionId=V` has also signed the GET
  of version `V`, and rgw-go serves that GET, as radosgw does, while it
  refuses the signer's own. s3-tests'
  `test_sigv2_presigned_upload_part_with_upload_id_packed_into_part_number`
  ([jhoblitt/ceph#6](https://github.com/jhoblitt/ceph/pull/6))
  packs an UploadPart's
  `uploadId` into its `partNumber`. Run against both releases' clusters on
  2026-10-14, radosgw answered it 400, as its PutObject reads the part
  number whenever one is present, and rgw-go 403.
- **A SigV2 request's unsigned query meta and encryption settings are not
  read.** The SigV2 string to sign takes a query map only on the presigned
  route
  ([rgw_auth_s3.cc:215-216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L215-L216),
  [rgw_auth_s3.cc:217-218](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L217-L218)),
  and that map holds the query's `x-amz-meta-*` and `x-amz-security-token`
  alone
  ([rgw_auth_s3.cc:175-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L175-L187),
  [rgw_auth_s3.cc:176-188](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L176-L188)).
  So a request signed in its Authorization header signs nothing of its
  query but the sub-resources, and neither route signs a query
  `x-amz-server-side-encryption*`. radosgw's PutObject, which also serves
  UploadPart, still runs `map_qs_metadata` with its encryption half on
  every route
  ([rgw_rest_s3.cc:2589-2603](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2589-L2603) and
  [rgw_rest_s3.cc:2619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2619),
  [rgw_rest_s3.cc:2742-2756](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2742-L2756) and
  [rgw_rest_s3.cc:2772](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2772)):
  it merges the query's `x-amz-meta-*` into the stored metadata and sets
  its `x-amz-server-side-encryption*` over the headers' with `OVERWRITE`.
  A header-signed request resent within its skew window, or a presigned
  URL resent before it expires, so gains what the holder adds there
  (`docs/ceph-upstream-bugs.md`, "[radosgw takes query meta and encryption
  settings a SigV2 request does not
  sign](ceph-upstream-bugs.md#radosgw-takes-query-meta-and-encryption-settings-a-sigv2-request-does-not-sign)").
  rgw-go, on both releases, reads none of the query meta or encryption
  settings the route's signature leaves out (`op.AuthResult.Route`,
  `op.AuthRoute.ReadsQueryMeta` and `ReadsQueryEncryption`), and a result
  that names no route reads neither:
  - on SigV2's header route a PutObject or an UploadPart stores none of the
    query's `x-amz-meta-*` (`requestAttrs`), where radosgw stores them and
    joins one naming a header's meta after the header's value;
  - on either SigV2 route the query's `x-amz-server-side-encryption*` sets
    none of the `s3:x-amz-server-side-encryption*` condition keys
    (`cryptAttr`), where radosgw's replaces the header's.

  A write naming encryption in the query is still answered 501, as every
  write that asks for encryption is in phase 1 ("Writes that ask for
  encryption answer 501, SSE-S3 and SSE-KMS until phase 2 and SSE-C until
  phase 4"), so nothing a client may have asked to have encrypted is
  stored in the clear; the phase that serves encryption must take a SigV2
  write's settings from its headers alone, as the condition keys already
  are. A presigned SigV2 request's query meta, which it signs and rgw-go
  folds into the headers ("A presigned SigV2 request's signed meta and
  session token mean the same in its headers and its query"), is read as
  before. SigV4, whose canonical query names each parameter, and anonymous
  requests, which sign nothing and may send the same names as headers,
  read the query as radosgw does. The cost: a client that signs a SigV2
  request in its header and sends its metadata in the query loses that
  metadata, with no error, and one that sends a SigV2 write's encryption
  settings in the query is answered 501, and later has the headers'
  settings alone applied. s3-tests'
  `test_sigv2_header_put_unsigned_meta_in_query`
  ([jhoblitt/ceph#9](https://github.com/jhoblitt/ceph/pull/9)) resends a
  header-signed PutObject with `?x-amz-meta-x=1`. Run against both
  releases' clusters on 2026-10-14, rgw-go stored the signed metadata
  alone, and radosgw stored `x` as well.
- **A SigV2 request's rgwx-uid is not read.** radosgw's `SysReqApplier`
  takes a system user's `rgwx-uid` from the query on every route, in its
  `load_acct_info`
  ([rgw_auth_filters.h:283-317](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_filters.h#L283-L317) at v19.2.7,
  [rgw_auth_filters.h:305-345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_filters.h#L305-L345) at v20.2.4),
  but SigV2 signs no `rgwx-` parameter: `RGWHTTPArgs::append` files them
  apart from the sub-resources the canonical resource writes and from the
  ordinary arguments the presigned route's query map reads
  ([rgw_common.cc:920-924](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L920-L924) at v19.2.7,
  [rgw_common.cc:933-937](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L933-L937) at v20.2.4).
  So whoever holds a system user's presigned SigV2 URL, or resends its
  header-signed request within the skew window, chooses the request's owner
  (`docs/ceph-upstream-bugs.md`, "[radosgw takes a system key's rgwx-uid
  from a query its signature does not
  cover](ceph-upstream-bugs.md#radosgw-takes-a-system-keys-rgwx-uid-from-a-query-its-signature-does-not-cover)").
  rgw-go, on both releases, takes a system user's `rgwx-uid` from a SigV4
  request alone, whose canonical query signs it (`Verifier.Authenticate`):
  a SigV2 system request runs as its own user, and an `rgwx-uid` naming no
  user or account is not looked up, where radosgw refuses it 403
  AccessDenied. Only radosgw's multisite connection sends `rgwx-uid`: a
  zone names the request's owner in it on every request it forwards to the
  metadata master and on an object fetch
  (`rgw_forward_request_to_master`,
  [rgw_op.cc:113-117](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L113-L117) at v19.2.7,
  [rgw_op.cc:147-152](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L147-L152) at v20.2.4;
  `RGWRESTConn::populate_params`,
  [rgw_rest_conn.cc:150-154](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_conn.cc#L150-L154) at v19.2.7,
  [rgw_rest_conn.cc:151-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_conn.cc#L151-L155) at v20.2.4),
  and it signs SigV2 only when `rgw_s3_client_max_sig_ver` is set to 1, 2
  or 3 (`sign_request`,
  [rgw_rest_client.cc:268-280](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_client.cc#L268-L280) at v19.2.7,
  [rgw_rest_client.cc:274-286](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_client.cc#L274-L286) at v20.2.4),
  where `rgw_s3_client_max_sig_ver` defaults to -1, SigV4
  ([rgw.yaml.in:1844-1849](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L1844-L1849) at v19.2.7,
  [rgw.yaml.in:1926-1931](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1926-L1931) at v20.2.4).
  A peer set so would have its forwarded and fetch requests run on rgw-go
  as the system user, a forwarded CreateBucket owned by it for one.
  Multisite is excluded, so nothing rgw-go serves today breaks; the gap is
  the multisite unit's to close.
- **A browser upload's identity takes nothing from the query.** radosgw
  authenticates a POST object form's credentials inside the op, through
  `create_apl_local`, which wraps the local applier in `SysReqApplier`
  ([rgw_auth_s3.h:186](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.h#L186) at v19.2.7,
  [rgw_auth_s3.h:188](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L188) at v20.2.4).
  For a system user's form its `load_acct_info` reads `rgwx-uid` from the
  request's query and makes the user or account it names the request's
  owner
  ([rgw_auth_filters.h:283-317](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_filters.h#L283-L317) at v19.2.7,
  [rgw_auth_filters.h:305-345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_filters.h#L305-L345) at v20.2.4),
  for whom `get_policy` then builds the stored object's ACL
  ([rgw_rest_s3.cc:3246-3247](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3246-L3247) at v19.2.7,
  [rgw_rest_s3.cc:3382-3383](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3382-L3383) at v20.2.4).
  The form's signature covers its policy document and nothing of the query
  (`AWSBrowserUploadAbstractor`,
  [rgw_rest_s3.cc:6157-6212](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6157-L6212) at v19.2.7,
  [rgw_rest_s3.cc:6687-6742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6687-L6742) at v20.2.4),
  so whoever holds a system user's signed form chooses that owner
  (`docs/ceph-upstream-bugs.md`, "[radosgw takes a system key's rgwx-uid
  from a query its signature does not
  cover](ceph-upstream-bugs.md#radosgw-takes-a-system-keys-rgwx-uid-from-a-query-its-signature-does-not-cover)").
  rgw-go's browser-upload identity takes nothing from the query, on both
  releases (`Verifier.AuthenticateBrowserUpload`, whose result's route,
  `op.RouteBrowserUpload`, reads none of it): a system user's form stores
  its object as the system user's own. Only radosgw's multisite connection
  sends `rgwx-` parameters, and it sends no form, so no client rgw-go
  serves loses one.
- **A SigV4 browser upload's credential must hold a slash.** radosgw splits
  a form's `x-amz-credential` at its first `/` into the key id and the
  scope, and for a credential without one takes the whole credential as
  both, as `substr(npos + 1)` is the whole string
  ([rgw_rest_s3.cc:6176-6181](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L6176-L6181) at v19.2.7,
  [rgw_rest_s3.cc:6706-6711](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6706-L6711) at v20.2.4),
  so it accepts a form signed under the signing key of that scope
  (`docs/ceph-upstream-bugs.md`, "[radosgw takes a SigV4 browser upload's
  credential without a slash as both its key id and its
  scope](ceph-upstream-bugs.md#radosgw-takes-a-sigv4-browser-uploads-credential-without-a-slash-as-both-its-key-id-and-its-scope)").
  AWS's credential is a key id and then its scope. rgw-go refuses such a
  form on both releases with 403 AccessDenied, radosgw's answer to every
  failed browser-upload authentication. No SDK signs a form so.
- **An MFA code the device may have accepted before does not verify.**
  radosgw checks an `x-amz-mfa` code with cls_otp, which stores for each
  success the check's TOTP step plus the window distance liboath found the
  code at, without its sign, and refuses a code whose index is at or below
  the last one stored
  ([cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/otp/cls_otp.cc#L129-L153),
  [cls_otp.cc:129-153](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/otp/cls_otp.cc#L129-L153)).
  A code checked in a step after its own therefore stores an index ahead of
  its step, and a used code verifies again once in each later step of its
  window, `window + 1` times in all, three at the defaults
  (`docs/ceph-upstream-bugs.md`, "[cls_otp computes the replay index from
  an unsigned window
  distance](ceph-upstream-bugs.md#cls_otp-computes-the-replay-index-from-an-unsigned-window-distance)").
  rgw-go sends the class the same check, so the otp object holds what
  radosgw's check leaves, but it reads the device's record first and
  guards the check on it. When the class verifies a code, rgw-go repeats
  liboath's walk over the device's seed from that record, to learn on which
  side of the check's step the code lay and how far
  (`internal/driver/mfa.go`). A code found ahead is of the step of its
  index, which the class found above the last success, and verifies. A
  code found behind is of the check's step less that distance, and
  verifies only when that step is above the last success the record held:
  every code the device accepted is of a step at or below the last success,
  and the record keeps no codes to tell which. So no code verifies twice
  through rgw-go, as RFC 6238 section 5.2 requires, and a code radosgw
  verified does not verify again through rgw-go. That holds while 0 <=
  secs - time_ofs < 2^32, with secs the check's time in seconds, where the
  class's unsigned step is liboath's signed one. Only an admin writing
  time_ofs into the device's record leaves that range: the class's index
  then follows no code's step and the class verifies a used code itself,
  and rgw-go gives the class's verdict.

  Of the codes radosgw verifies, rgw-go refuses exactly these, wherever its
  walk finds the code: a code found behind the check's step whose step is
  at or below the device's last success before the check. Each is either
  used again or fresh, and the record cannot tell which. Among the fresh
  ones:
  - a code of a step before the check's, after a code used late raised the
    last success past it: the code of step n used in step n+1 stores n+2,
    and the code of step n+1 used next in step n+2 is refused;
  - an unused code of an earlier step, after a newer code: the code of step
    n used in step n, then the code of step n-1 in the same step;
  - the codes of an authenticator d steps behind, or of a user who reads
    each code d steps late. The class records each refused code as a
    success all the same, and raises the last success to its check's step
    plus d, so at a cadence of 2d steps or less every request after the
    first is refused, where radosgw verifies every one.

  `radosgw-admin mfa resync` cures only part of that. Given two consecutive
  codes, it probes time offsets in whole steps and stores the fit less a
  step plus step_size * window / 2
  ([rgw_admin.cc:3074-3105](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L3074-L3105),
  [radosgw-admin.cc:3271-3302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L3271-L3302);
  the command,
  [rgw_admin.cc:10965-11028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_admin.cc#L10965-L11028),
  [radosgw-admin.cc:11557-11621](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/radosgw-admin/radosgw-admin.cc#L11557-L11621)):
  a whole number of steps, and half a step more at an odd window. So it
  corrects a drift of whole steps at every window but 1: there the half step
  can leave the device ahead of the authenticator, while at windows 3 and 5
  it leaves the device behind it, where its codes are found ahead. An
  authenticator a fraction of a step behind, or a code that reaches the
  gateway after its step has ended, can still be refused at a cadence of two
  steps or less: at window 2, an authenticator 5 s behind, used once a step
  or once every two steps in the first 5 s of each step, is refused 7 times
  in 8, where the class verifies all 8, and once every three steps is
  verified every time. That is on the offset resync usually leaves for that
  skew, 0, which a scan run in seconds 5 to 29 of a step stores; a scan in
  the first 5 s stores 60 s, after which every code verifies. Short of that,
  only setting the authenticator's clock right, or an admin writing
  time_ofs into the device's record, ends it. radosgw is not free of it either: after a code
  that arrives in a step after its own, its class refuses the next step's
  current code, a fresh one, as the late code's index is its check's step
  plus one (`docs/ceph-upstream-bugs.md`, "[cls_otp computes the replay index
  from an unsigned window
  distance](ceph-upstream-bugs.md#cls_otp-computes-the-replay-index-from-an-unsigned-window-distance)").

  A code the class's liboath finds and rgw-go's walk does not, which only a
  liboath that computes otherwise than RFC 6238 would give, falls back to
  the index the check stored. rgw-go then also refuses a code of a step
  after the check's within that distance of the last success, and, while
  another check's success lands between its check and its lookup, a code
  whose check's step less the distance that success's index shows is at or
  below the last success; on a class with ceph/ceph#72253, whose index is
  the code's own step, a code checked after its step verifies.

  Each check costs three round trips, the record's read, the guarded check
  and the lookup, where radosgw sends two. A record that changes between
  the read and the check, as each recorded check changes it, at most five
  times in a device's step_size seconds, cancels the check, and rgw-go
  reads the record and sends the check again, six times at most, after
  which the verification fails with an error. rgw-go sends no check at all
  for a device outside the otp put's bounds ("A stored MFA device outside
  the otp put's bounds does not verify", next). The refusal sees only the
  zone's otp object: a code rgw-go verified still verifies through radosgw
  on the same zone in the later steps of its window, and once at each other
  zone
  ("[radosgw's otp replay state stays in the zone that recorded
  it](ceph-upstream-bugs.md#radosgws-otp-replay-state-stays-in-the-zone-that-recorded-it)").
- **A stored MFA device outside the otp put's bounds does not verify.**
  rgw-go's MFA check reads the device's record first, and for a device the
  otp metadata put would refuse ("The admin metadata API serves four
  sections") answers not verified, sending the class nothing
  (`uncheckedOTPDevice`, `internal/driver/mfa.go`). radosgw checks every
  stored device as the class finds it:
  - an empty key, which the class parses from an empty seed, a hex seed led
    by a NUL or a base32 seed of nothing but spaces, verifies codes anyone
    can make from the clock alone
    ("[radosgw's otp metadata put stores a device without a seed, whose
    codes anyone can
    make](ceph-upstream-bugs.md#radosgws-otp-metadata-put-stores-a-device-without-a-seed-whose-codes-anyone-can-make)");
  - a step of 0 faults the OSD for a good code, and records a failed check
    for a wrong one
    ("[cls_otp divides by a stored step_size that is never
    validated](ceph-upstream-bugs.md#cls_otp-divides-by-a-stored-step_size-that-is-never-validated)");
  - a step over 3600 seconds verifies codes as any other step does, until
    the step passes the clock's seconds, where the class keeps no attempt
    limit and verifies no code
    ("[cls_otp never verifies a device whose step is longer than the
    clock's
    seconds](ceph-upstream-bugs.md#cls_otp-never-verifies-a-device-whose-step-is-longer-than-the-clocks-seconds)");
  - a window over 1024 steps costs the OSD a walk of 2·window+1 codes for
    each wrong code
    ("[cls_otp verifies any stored window, an OSD CPU cost per
    check](ceph-upstream-bugs.md#cls_otp-verifies-any-stored-window-an-osd-cpu-cost-per-check)").

  So a request whose MFA delete stands on such a device is refused through
  rgw-go with radosgw's answer to a code that does not verify. A device
  rgw-go's put wrote is never among them, and an otp put of the user's
  devices within the bounds replaces one radosgw wrote.

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
  `:1541-1547`), judging the policy public as Amazon S3 does ("A bucket
  policy is public as Amazon S3 judges it", below).
  Squid's radosgw ignores both (`docs/ceph-upstream-bugs.md`, "Squid
  accepts RestrictPublicBuckets but never enforces it"). rgw-go also lets an
  admin user that is not a system user through a suspended bucket's
  refusal and an unparsable bucket policy's, as v20.2.4 does
  (`rgw_op.cc:396`, `:433` and `:642` at v20.2.4); Squid's radosgw exempts
  only system users (`:366`, `:403` and `:612` at v19.2.6). The policy
  language itself, its actions, operators and principals, follows the
  cluster's release.
- **A bucket policy is public as Amazon S3 judges it.** radosgw's
  `is_public` evaluates a statement granting every principal against three
  fixed keys, aws:SourceIp `1.1.1.1`, aws:UserId `anonymous` and a KMS key
  id
  ([rgw_iam_policy.cc:1894-1907](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1894-L1907),
  [rgw_iam_policy.cc:1929-1942](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1929-L1942)),
  so a condition on any other key, such as `Bool aws:SecureTransport true`,
  makes the grant not public and one 1.1.1.1 meets makes it public; Squid's
  also counts every other Allow statement whose NotPrincipal names no
  wildcard, a grant to one user among them (`none_of` over `noprinc`,
  [rgw_iam_policy.cc:1909-1912](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1909-L1912))
  (`docs/ceph-upstream-bugs.md`, "radosgw's is_public judges a
  wildcard-principal statement against a fixed three-key environment" and
  "Squid's is_public counts every Allow statement whose NotPrincipal is not
  the wildcard"). rgw-go judges a policy on both releases as Amazon S3's
  "The meaning of "public""
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/access-control-block-public-access.html#access-control-block-public-access-policy-status)
  does, evaluating no condition (`policy.Policy.IsPublic`). A policy is
  public when an Allow statement grants every principal, and none of its
  conditions limits the grant to fixed values of a key that page lists. A
  statement grants every principal through the wildcard in its Principal; a
  principal there whose name holds a wildcard, `*` or `?`, or a policy
  variable; a principal that admits the anonymous requester, the default
  tenant's account, as `arn:aws:iam:::root` or `""`, or the user
  `arn:aws:iam:::user/anonymous` (`docs/ceph-upstream-bugs.md`,
  "Tentacle's is_public misses a fixed principal that admits the anonymous
  requester"); or a NotPrincipal without the wildcard. A fixed value holds
  no wildcard of the matching operators, `*`, `?`, `[` or `\`, and no policy
  variable. The listed keys are aws:SourceIp, each range no
  broader than an IPv4 /8 or an IPv6 /32; aws:SourceArn, aws:SourceVpc,
  aws:SourceVpce, aws:SourceOwner, aws:SourceAccount, aws:userid,
  aws:PrincipalOrgID and s3:DataAccessPointAccount; and
  s3:DataAccessPointArn, whose access point name alone may hold a wildcard
  when the account is fixed. Keys match in any ASCII case. IpAddress limits
  aws:SourceIp; the string and ARN equality and matching operators and
  their ForAnyValue forms limit the rest; IfExists, ForAllValues, the
  negated operators and a condition with no value limit nothing. A Deny
  statement, and what a statement's actions and resources name, never
  count. An IPv6 range holding ::/96 or ::ffff:0:0/96 holds every IPv4
  address, since radosgw compares an IPv4 address with an IPv6 range as
  ::a.b.c.d (`MaskedIP`'s `operator ==`,
  [rgw_iam_policy.h:341-346](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L341-L346),
  [rgw_iam_policy.h:360-365](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L360-L365)).
  The judgment decides PutBucketPolicy's BlockPublicPolicy refusal,
  RestrictPublicBuckets and GetBucketPolicyStatus's IsPublic. Under a block
  of public policies rgw-go therefore differs from radosgw as follows:
  - It refuses with 403 AccessDenied, on both releases, a grant to everyone
    under a condition that limits nothing by that rule and that the three
    keys fail, such as `Bool aws:SecureTransport true`, which radosgw stores
    on both.
  - It refuses a grant to a principal whose name holds a wildcard or a
    policy variable, such as `arn:aws:iam:::user/alice:*`, every subuser of
    alice, or `arn:aws:iam:::user/${aws:username}`. Squid's radosgw refuses
    it too, through its count of every named principal, and Tentacle's
    stores it.
  - It refuses a grant to a principal that admits the anonymous requester.
    Squid's radosgw refuses it too, for the same reason, and Tentacle's
    stores it.
  - It stores a grant to everyone limited to fixed values of a listed key
    that the three keys meet, which radosgw refuses on both releases: a
    range holding 1.1.1.1, such as `IpAddress aws:SourceIp 1.1.1.1/32`, or
    `StringEquals aws:UserId anonymous`, whose key matches aws:userid in any
    case.
  - On Squid it stores a grant to fixed named principals, which Squid's
    radosgw refuses.

  Under RestrictPublicBuckets, which Squid's radosgw never enforces, rgw-go
  refuses every requester outside the bucket owner's account a grant that
  it judges public and v20.2.4 does not: a grant to everyone under
  `Bool aws:SecureTransport true`; a grant to `user/alice:*`, whose
  subusers v20.2.4 serves when another user owns the bucket; and a grant to
  `arn:aws:iam:::root`, whose anonymous and default-tenant requesters
  v20.2.4 serves. It serves two grants that v20.2.4 restricts: a grant to
  everyone from 1.1.1.1/32 alone, and one under
  `StringEquals aws:UserId anonymous`, which grants no one in rgw-go, whose
  environment holds neither aws:UserId nor aws:userid.

  GetBucketPolicyStatus reports a bucket public when its policy is, by this
  judgment, or its ACL is, whatever its public-access block sets, as
  radosgw joins its own two answers
  ([rgw_op.cc:8608-8611](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8608-L8611),
  [rgw_op.cc:9576-9579](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9576-L9579)).
  Wherever the two judgments disagree on a release, as the cases above
  show, a bucket whose ACL is not public gets the opposite IsPublic from
  rgw-go and from that release's radosgw. So s3-tests'
  `test_get_nonpublicpolicy_principal_bucket_policy_status`, a grant to
  `arn:aws:iam::s3tenant1:root`, reports false on both releases, where
  Squid's radosgw reports true; a grant to everyone under
  `Bool aws:SecureTransport true` reports true, where radosgw reports false
  on both; and a grant to everyone from 1.1.1.1/32 alone reports false,
  where radosgw reports true on both.
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
- **A copy source needs READ in its bucket's ACL and in its own.** When no
  bucket or identity policy decides, radosgw authorizes CopyObject's source
  against the source bucket's ACL alone, and reads the source object's ACL
  without using it (`rgw_op.cc:5409-5459` at v19.2.6, `:5975-6025` at
  v20.2.4; `docs/ceph-upstream-bugs.md`, "[radosgw checks a CopyObject
  source against its bucket's ACL, not the
  object's](ceph-upstream-bugs.md#radosgw-checks-a-copyobject-source-against-its-buckets-acl-not-the-objects)").
  So a grantee of READ on the bucket, which for S3 is leave to list it, can
  copy any object in it, one whose own ACL refuses them included. rgw-go
  checks the source against the bucket's ACL and then
  against the object's, and needs READ in both. Policies decide first, as
  in radosgw, so a policy that allows or denies the read decides it. A
  grantee of the bucket alone is refused where radosgw copies; a grantee of
  the object alone is refused by both; rgw-go allows no copy radosgw
  refuses. UploadPartCopy's source is checked the other way round: radosgw
  authorizes it against the source object's ACL. With
  `rgw_defer_to_bucket_acls` at its default, empty, the bucket's ACL counts
  only for Swift's READ_OBJS, which no S3 grant holds; that option set lets
  the bucket's ACL grant first (`check_deferred_bucket_only_acl`,
  `rgw_common.cc:1567-1570` at v19.2.6, `:1621-1624` at v20.2.4)
  (`rgw_op.cc:3956-3961` at v19.2.6, `:4165-4170` at v20.2.4;
  `verify_object_permission_no_policy`, `rgw_common.cc:1560-1608` at
  v19.2.6, `:1614-1671` at v20.2.4). rgw-go makes the same two checks for
  UploadPartCopy as for CopyObject, so there a grantee of the object alone
  is refused where radosgw copies, and a grantee of the bucket alone is
  refused by both.
- **A block of public ACLs refuses a public ACL on PutObject, CopyObject,
  CreateMultipartUpload and UploadPart.** radosgw enforces a bucket's
  BlockPublicAcls on PutObject and UploadPart, both `RGWPutObj`, only for
  the canned ACLs `public-read`, `public-read-write` and
  `authenticated-read` (`rgw_op.cc:3903-3909` at v19.2.6, `:4112-4118` at
  v20.2.4), so a public policy built from `x-amz-grant-*` headers is
  stored; CopyObject and CreateMultipartUpload, `RGWOp`s of their own,
  check no block at all (`docs/ceph-upstream-bugs.md`, "[radosgw lets a
  public ACL through a block of public ACLs on PutObject's grant headers,
  CopyObject and
  CreateMultipartUpload](ceph-upstream-bugs.md#radosgw-lets-a-public-acl-through-a-block-of-public-acls-on-putobjects-grant-headers-copyobject-and-createmultipartupload)").
  rgw-go refuses with 403 AccessDenied, once the requester is authorized
  ("PutObject and CopyObject authorize a request before they read what the
  store holds for it"; "The multipart routes authorize a request before
  they read what the store holds for it"), a PutObject or CopyObject whose
  new object's policy, a CreateMultipartUpload whose upload's policy, which
  the completed object takes, or an UploadPart whose part's policy grants
  AllUsers or AuthenticatedUsers anything under such a block, as
  PutObjectAcl's
  `is_public` check does (`:5905-5910` at v19.2.6). It also takes a block
  that does not decode for one that blocks public ACLs in these writes, in
  PutBucketAcl and in PutObjectAcl, and for one
  that blocks public policies in PutBucketPolicy
  (`rgw_op.cc:8103-8108` at v19.2.6, `:9029-9034` at v20.2.4), where
  radosgw takes it for none; the authorizer refuses such a bucket's
  requests from anyone but an admin ("A public-access block that does not
  decode refuses the request"), so only an admin meets these refusals.
- **IgnorePublicAcls sets aside a Swift container's grants to everyone, and
  an ACL holding them is public.** radosgw's IgnorePublicAcls, `get_perm`'s
  `ignore_public_acls`, skips the AllUsers and AuthenticatedUsers groups but
  not the referer grants a Swift container ACL holds, whose lookup,
  `get_referer_perm`, runs whatever it says
  ([rgw_acl.cc:180-192](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L180-L192),
  [rgw_acl.cc:180-192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L180-L192)).
  With `rgw_enforce_swift_acls` set, as it is by default
  ([rgw_common.cc:1581](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1581),
  [rgw_common.cc:1638](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1638)),
  an object read the object's ACL refuses falls back to the bucket ACL's
  Swift flags, `bucket_acl.verify_permission` for `swift_perm`, with
  IgnorePublicAcls left off
  ([rgw_common.cc:1598-1599](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1598-L1599),
  [rgw_common.cc:1655-1656](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1655-L1656))
  (`docs/ceph-upstream-bugs.md`, "radosgw's IgnorePublicAcls and ACL
  is_public miss Swift's grants to everyone"). So a container a Swift client
  opened with `X-Container-Read: .r:*` serves anonymous S3 GetObject under
  IgnorePublicAcls, and ListBucket to a request that sends a Referer. rgw-go
  reads no referer grant under IgnorePublicAcls, and its Swift fallback
  honours IgnorePublicAcls as its bucket and object checks do
  (`acl.Policy.Perm`; `objectACL`, `internal/authz/evaluator.go`), so on
  both releases it refuses those requests with 403 AccessDenied. It answers
  403 too to a GET of a missing key that sends a Referer, which radosgw
  answers 404 NoSuchKey because the referer grant lets the requester list
  the bucket. The client chooses the Referer, so a referer grant is a grant
  to everyone; a negative one,
  `.r:-host`, is set aside too, and a request naming its host gets what the
  same request without a Referer gets. A matching referer grant therefore
  replaces none of the requester's own grants under IgnorePublicAcls
  (`docs/ceph-upstream-bugs.md`, "radosgw lets a matching referer grant
  replace the requester's own ACL grants"): the owner's S3 PutObject with a
  Referer that `.r:example.com` matches succeeds there, where radosgw
  refuses it. `acl.Policy.IsPublic`, which decides the BlockPublicAcls
  refusals, counts any flag the group map gives AllUsers or
  AuthenticatedUsers, Swift's READ_OBJS and WRITE_OBJS included, and any
  referer grant; radosgw's `is_public` masks the groups' flags with
  FULL_CONTROL and calls such a container private
  ([rgw_acl.cc:235-247](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L235-L247),
  [rgw_acl.cc:235-247](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L235-L247)).
  Since BC-20 GetBucketPolicyStatus's IsPublic takes the same judgment
  (`op.GetBucketPolicyStatus`, `internal/op/policystatus.go`): for a bucket
  whose policy is not public, an ACL that only Swift's flags or a referer
  grant make public is reported IsPublic true, where radosgw, which joins
  the policy's `is_public` and the ACL's
  ([rgw_op.cc:8608-8611](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8608-L8611),
  [rgw_op.cc:9576-9579](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9576-L9579)),
  reports false.
  An S3 request can write neither those flags nor a referer grant, so on
  the ACL an S3 write builds the two judgments agree.
- **IgnorePublicAcls sets aside grants to the user anonymous and its
  ownership.** Every unauthenticated request runs as the user `anonymous`
  (`rgw_get_anon_user`,
  [rgw_user.cc:106-111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_user.cc#L106-L111),
  [rgw_user.cc:106-111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_user.cc#L106-L111)),
  and `get_perm` reads that user's entry in the ACL's user map, and its
  ownership, whatever `ignore_public_acls` says
  ([rgw_acl.cc:169-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L169-L187),
  [rgw_acl.cc:169-187](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L169-L187))
  (`docs/ceph-upstream-bugs.md`, "radosgw's IgnorePublicAcls and ACL
  is_public miss grants to the anonymous user"). Such a grant comes from a
  Swift container ACL naming `anonymous`, which radosgw stores whether or
  not that user exists, and from an anonymous S3 write: its canned ACL
  grants the writer FULL_CONTROL, and its grant headers make the writer the
  object's owner. Under IgnorePublicAcls rgw-go sets aside the user map's
  entry for `anonymous` and, on an ACL that user owns, the owner's READ_ACP
  and WRITE_ACP, in every check, the Swift fallback included
  (`acl.Policy.Perm`). On both releases it therefore refuses with 403
  AccessDenied every anonymous request that radosgw serves through such a
  grant or such ownership, whichever op asks. That includes every op that
  authorizes through the bucket's WRITE_ACP or READ_ACP, since radosgw
  counts the Swift write list's WRITE_OBJS as WRITE and WRITE_ACP, and the
  read list's READ_OBJS as READ and READ_ACP
  ([rgw_acl.cc:216-221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L216-L221),
  [rgw_acl.cc:216-221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L216-L221)).
  In radosgw, under all four block settings, `X-Container-Write: anonymous`
  lets any unauthenticated request delete the bucket's public access block,
  which `op_to_perm` maps to WRITE_ACP
  ([rgw_iam_policy.h:310-311](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L310-L311),
  [rgw_iam_policy.h:327-328](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L327-L328)),
  and then install a public bucket policy (`docs/ceph-upstream-bugs.md`,
  "radosgw lets an ACL grant change a bucket's public access block"). rgw-go
  refuses that bucket policy write, and every other op it serves, to such a
  grant, and no ACL grant authorizes the block's own ops ("No ACL grant
  authorizes the public access block's ops", below). A grant to a tenant's user named
  `anonymous` admits no one in either gateway: neither can load or create a
  user with that id in any tenant.
- **BlockPublicAcls counts grants to the user anonymous and its ownership,
  but for an anonymous writer's own.** `acl.Policy.IsPublic` counts as
  public any flag the user map gives `anonymous` and an ACL that user owns,
  neither of which radosgw's `is_public` reads
  ([rgw_acl.cc:235-247](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L235-L247),
  [rgw_acl.cc:235-247](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L235-L247)).
  When an anonymous request writes a new object, upload or part, the
  FULL_CONTROL its canned ACL gives it and the ownership its grant headers
  give it are its own and do not count (`acl.Policy.ForOwnWrite`, which
  `authz.DefaultACL` applies), as AWS refuses such a write only when "the
  request includes a public ACL": an anonymous PutObject naming no public
  ACL is stored under BlockPublicAcls, as radosgw stores it. Every other
  grant to `anonymous` counts. Under BlockPublicAcls rgw-go therefore
  refuses with 403 AccessDenied, where radosgw stores it, a PutObjectAcl
  from any requester on an object the anonymous user owns, whose ACL keeps
  that owner. An S3 grant naming `id=anonymous` is no difference: both
  gateways refuse it alike, 400 InvalidArgument in an ACL document and 404
  NoSuchKey in a grant header, as neither loads a user of that id
  (`read_user_info`,
  [svc_user_rados.cc:125-128](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_user_rados.cc#L125-L128),
  [svc_user_rados.cc:106-109](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_user_rados.cc#L106-L109);
  `driver.Store.GetUser`). A stored ACL decodes without the writer's mark,
  so its judgment counts every grant. Since BC-20 GetBucketPolicyStatus's
  IsPublic takes the same judgment of the stored bucket ACL: for a bucket
  whose policy is not public, an ACL that only a grant to `anonymous` or
  that user's ownership makes public is reported IsPublic true, where
  radosgw, which joins the policy's `is_public` and the ACL's
  ([rgw_op.cc:8608-8611](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8608-L8611),
  [rgw_op.cc:9576-9579](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9576-L9579)),
  reports false.
- **No ACL grant authorizes the public access block's ops.** radosgw
  authorizes PutPublicAccessBlock and DeletePublicAccessBlock as
  s3:PutBucketPublicAccessBlock, and GetPublicAccessBlock as
  s3:GetBucketPublicAccessBlock, through `verify_bucket_permission`
  ([rgw_op.cc:8619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8619),
  [rgw_op.cc:8733](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8733),
  [rgw_op.cc:8683](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8683);
  [rgw_op.cc:9587](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9587),
  [rgw_op.cc:9701](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9701),
  [rgw_op.cc:9651](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9651)), which, where no policy decides,
  falls back to the bucket's ACL with the permission `op_to_perm` maps the
  action to
  ([rgw_common.cc:1370-1371](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1370-L1371),
  [rgw_common.cc:1392-1393](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1392-L1393)): WRITE_ACP for
  the two writes and READ_ACP for the read
  ([rgw_iam_policy.h:310-311](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L310-L311),
  [rgw_iam_policy.h:288-289](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L288-L289);
  [rgw_iam_policy.h:327-328](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L327-L328),
  [rgw_iam_policy.h:304-305](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L304-L305)). So any
  WRITE_ACP grantee can change or remove the block, and any READ_ACP
  grantee can read it: a named user granted either or FULL_CONTROL; another
  account granted them, for its root with no identity policy, whose
  cross-account check's identity side allows an account root on its own
  ([rgw_common.cc:1241-1243](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1241-L1243),
  [rgw_common.cc:1387-1396](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1387-L1396);
  [rgw_common.cc:1254-1256](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1254-L1256),
  [rgw_common.cc:1415-1426](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1415-L1426)) while the
  resource side matches the grant to the account
  ([rgw_auth.cc:1032-1046](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth.cc#L1032-L1046),
  [rgw_auth.cc:1052-1066](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth.cc#L1052-L1066)), and for its other
  users with an identity policy that allows the action; AllUsers or
  AuthenticatedUsers granted them while IgnorePublicAcls is off
  ([rgw_acl.cc:180-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L180-L187),
  [rgw_acl.cc:180-187](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L180-L187)); for the read, the
  grantees of a Swift read list, whose READ_OBJS the ACL check counts as READ
  and READ_ACP
  ([rgw_acl.cc:219-221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L219-L221),
  [rgw_acl.cc:219-221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L219-L221)): its named users, `.r:*`,
  which is everyone, and a referer grant, which any client matches with its
  own Referer and which IgnorePublicAcls does not set aside
  ([rgw_acl.cc:190-191](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L190-L191),
  [rgw_acl.cc:190-191](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L190-L191)); and under all four
  settings the user `anonymous` through a Swift
  `X-Container-Write: anonymous` list, whose WRITE_OBJS
  ([rgw_acl_swift.cc:22](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_swift.cc#L22),
  [rgw_acl_swift.cc:22](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl_swift.cc#L22)) the ACL check counts as
  WRITE and WRITE_ACP
  ([rgw_acl.cc:216-218](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L216-L218),
  [rgw_acl.cc:216-218](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L216-L218))
  (`docs/ceph-upstream-bugs.md`, "radosgw lets an ACL grant change a
  bucket's public access block"). Amazon S3 maps a bucket's WRITE_ACP to
  s3:PutBucketAcl alone and its READ_ACP to s3:GetBucketAcl alone
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping),
  so no ACL grant reaches the block's actions. rgw-go authorizes the three
  ops by identity and bucket policy, evaluated as for any other bucket op,
  and where no policy decides lets through the bucket's owner, the owner of
  its ACL, within the key's permission mask, and refuses everyone else with
  403 AccessDenied (`authz.Evaluator.VerifyBucketOwnerOrPolicy`,
  `internal/authz/evaluator.go`; `internal/op/publicaccess.go`). The
  owner's pass is the one radosgw's ACL gives its owner, READ_ACP and
  WRITE_ACP whatever its grants say
  ([rgw_acl.cc:171-173](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L171-L173),
  [rgw_acl.cc:171-173](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L171-L173)), with two differences.
  IgnorePublicAcls sets an anonymous owner's aside, as every rgw-go check
  does ("IgnorePublicAcls sets aside grants to the user anonymous and its
  ownership", above). And no referer grant replaces it, where radosgw
  refuses the owner whenever a referer grant matching the request's Referer
  lacks the permission: a write under any matching referer grant, a read
  under a negative one (`docs/ceph-upstream-bugs.md`, "radosgw lets a
  matching referer grant replace the requester's own ACL grants"). An
  account's own users are decided by policy alone in both gateways, and an
  admin is let through as after any access denial. So every grantee above
  is refused where radosgw serves it. Beyond the policy judgments other
  exclusions record, the one request rgw-go serves that radosgw refuses is
  the owner's under such a referer grant. One such judgment reaches these
  ops too: on Tentacle, under RestrictPublicBuckets, rgw-go serves a
  non-owner through a policy Amazon S3 calls not public and radosgw's
  is_public calls public, such as a grant to everyone from 1.1.1.1/32 alone,
  which radosgw refuses ("A bucket policy is public as Amazon S3 judges it",
  above).
- **An object ACL's WRITE grant authorizes nothing but its owner's own
  requests.** `op_to_perm` gives RGW_PERM_WRITE to the object-scope writes
  of an object's retention, legal hold and governance bypass, of its tags in
  both forms, and of its restore
  ([rgw_iam_policy.h:260-268](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L260-L268),
  [rgw_iam_policy.h:273-284](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L273-L284)),
  and PutObjectRetention, its bypass, PutObjectLegalHold, PutObjectTagging
  and DeleteObjectTagging check those actions on the object
  ([rgw_op.cc:8340](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8340),
  [rgw_op.cc:8348](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8348),
  [rgw_op.cc:8492](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8492),
  [rgw_op.cc:1088](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1088),
  [rgw_op.cc:1127](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1127);
  [rgw_op.cc:9275](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9275),
  [rgw_op.cc:9283](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9283),
  [rgw_op.cc:9440](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9440),
  [rgw_op.cc:1287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1287),
  [rgw_op.cc:1345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1345)).
  Where no policy decides, `verify_object_permission_no_policy` grants on
  the object ACL's WRITE whoever holds it, with no exception for any action
  ([rgw_common.cc:1572-1579](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1572-L1579),
  [rgw_common.cc:1626-1636](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1626-L1636)),
  and with `rgw_enforce_swift_acls` on the WRITE_OBJS of a Swift write list
  in the bucket's ACL
  ([rgw_common.cc:1587-1602](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1587-L1602),
  [rgw_common.cc:1644-1662](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1644-L1662)).
  The canned `public-read-write` grants AllUsers WRITE on an object as on a
  bucket
  ([rgw_acl_s3.cc:428-433](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_s3.cc#L428-L433),
  [rgw_acl_s3.cc:428-433](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl_s3.cc#L428-L433)).
  So in radosgw any grantee of an object's WRITE, the anonymous user through
  `public-read-write` included, can shorten or bypass its GOVERNANCE
  retention, lock it in COMPLIANCE until 2554, release its legal hold and
  rewrite its tags. Only these stop it: a Deny of the action in a bucket or
  identity policy, which the policy evaluation decides before the ACL
  fallback is read
  ([rgw_common.cc:1503-1515](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1503-L1515),
  [rgw_common.cc:1549-1561](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1549-L1561));
  BlockPublicAcls, which keeps a public canned ACL such as
  `public-read-write` from being stored on a new write, and a public ACL
  from being set by PutObjectAcl
  ([rgw_op.cc:3903-3909](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3903-L3909),
  [rgw_op.cc:5908-5913](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5908-L5913);
  [rgw_op.cc:4112-4118](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4112-L4118),
  [rgw_op.cc:6551-6556](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6551-L6556));
  and IgnorePublicAcls, which sets aside the public grants an object
  already holds, though not a named grantee's
  (`docs/ceph-upstream-bugs.md`, "[radosgw lets an object ACL's WRITE grant
  set, shorten and bypass an object's retention and legal
  hold](ceph-upstream-bugs.md#radosgw-lets-an-object-acls-write-grant-set-shorten-and-bypass-an-objects-retention-and-legal-hold)").
  Amazon S3 calls an object ACL's WRITE "not applicable", and maps no object
  ACL permission to these actions
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping).
  rgw-go's object-ACL fallback grants WRITE to the object's owner alone, the
  owner its ACL names, through the owner's own grant and within the key's
  permission mask, and never to the anonymous user, an anonymous writer's
  object included. Every other grantee of an object's WRITE or FULL_CONTROL,
  a named user, AllUsers, AuthenticatedUsers, an email grantee, and the
  grantees of a Swift write list, is refused with 403 AccessDenied (the
  object-ACL leg of `authz.Evaluator`, `internal/authz/evaluator.go`). That
  includes the bucket's owner on an object another user or account wrote
  with `bucket-owner-full-control`, whose FULL_CONTROL grant to the bucket
  owner
  ([rgw_acl_s3.cc:444-449](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl_s3.cc#L444-L449),
  [rgw_acl_s3.cc:444-449](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl_s3.cc#L444-L449))
  lets it tag the object and set its retention and legal hold in radosgw:
  in rgw-go that owner needs a bucket or identity policy allowing the
  action, as in Amazon S3, where no ACL reaches these actions. A policy
  decides as before, and first; the READ, READ_ACP and WRITE_ACP an
  object's grant gives are unchanged; and the bucket-scope WRITE that
  PutObject and DeleteObject check is still the bucket ACL's, as in radosgw.
- **rgw-go stores and enforces ownership controls a floor radosgw ignores.**
  Neither floor has ownership controls: no op serves them
  ("ownershipControls requests are served as v21 serves them", above), and
  no check reads the setting. A floor's bucket writes carry
  `user.rgw.ownership-controls` through as an attr they do not know, as
  they lay their own attrs over the bucket's
  ([rgw_sal_rados.cc:771-778](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L771-L778),
  [rgw_sal_rados.cc:789-796](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L789-L796)),
  but for a retry that writes back the attrs its request loaded. A floor's
  PutBucketPolicy, PutPublicAccessBlock, PutBucketCors or
  PutBucketVersioning that races an rgw-go ownership write can put back the
  setting its request loaded, turning BucketOwnerEnforced back into
  ObjectWriter while both requests are told they succeeded
  (`docs/ceph-upstream-bugs.md`, "[radosgw's PutBucketPolicy retry writes
  back the bucket attrs its request started
  with](ceph-upstream-bugs.md#radosgws-putbucketpolicy-retry-writes-back-the-bucket-attrs-its-request-started-with)").
  rgw-go serves Get, Put and DeleteBucketOwnershipControls on both releases
  as v21.1.2, the first release line to carry them, does
  (`internal/op/ownership.go`):
  - GetBucketOwnershipControls answers a bucket without the setting 404
    OwnershipControlsNotFoundError, "The bucket ownership controls were not
    found", and one whose setting does not decode 500
    ([rgw_op.cc:10315-10334](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10315-L10334));
  - PutBucketOwnershipControls stores the setting alone, in v21.1.2's (1,1)
    form, which a zone's radosgw reads once upgraded to v21. It refuses
    BucketOwnerEnforced over a bucket ACL other than the default private
    one of the owner the ACL names with 400
    InvalidBucketAclWithObjectOwnership, "The bucket's ACL must be made
    private before setting BucketOwnerEnforced.", and over one that does not
    decode with 500
    ([rgw_op.cc:10255-10305](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10255-L10305));
  - DeleteBucketOwnershipControls removes it, and succeeds on a bucket
    without one
    ([rgw_op.cc:10344-10358](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10344-L10358)).

  Under BucketOwnerEnforced, rgw-go refuses PutBucketAcl and PutObjectAcl
  with 400 AccessControlListNotSupported, "Cannot set ACLs when
  ObjectOwnership is BucketOwnerEnforced.", before it reads the body, as
  v21.1.2 does
  ([rgw_op.cc:6866-6871](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L6866-L6871)), and so does each
  retry of a lost race against the setting that retry read, where v21.1.2
  retries neither write. A floor radosgw stores the ACL: its `RGWPutACLs`
  has no such check
  ([rgw_op.cc:5824-5930](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5824-L5930),
  [rgw_op.cc:6467-6590](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6467-L6590)).
  CreateBucket stores the setting a request names, held to the same private
  ACL under BucketOwnerEnforced ("CreateBucket with x-amz-object-ownership
  is authorized as s3:PutBucketOwnershipControls too", above). Amazon S3
  refuses both ACL writes: "Requests to set ACLs or update ACLs fail and
  return the AccessControlListNotSupported error code"
  ([api.go:8036-8041](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L8036-L8041),
  [api.go:10562-10567](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L10562-L10567)).

  Its three ops differ from v21.1.2's in two ways. No ACL grant authorizes the
  three. v21.1.2 falls back to the bucket's ACL with the READ_ACP and
  WRITE_ACP its `op_to_perm` maps the two actions to
  ([rgw_iam_policy.h:314-315](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L314-L315),
  [rgw_iam_policy.h:338-339](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_iam_policy.h#L338-L339)), so any
  WRITE_ACP grantee of the bucket can turn BucketOwnerEnforced off, and
  with it the ACLs back on (`docs/ceph-upstream-bugs.md`, "[v21's ownership
  controls ops let an ACL grant change a bucket's object
  ownership](ceph-upstream-bugs.md#v21s-ownership-controls-ops-let-an-acl-grant-change-a-buckets-object-ownership)").
  Amazon S3 maps those two ACL permissions to s3:GetBucketAcl and
  s3:PutBucketAcl alone
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/acl-overview.html#acl-access-policy-permission-mapping).
  rgw-go authorizes the three as it authorizes the public access block's
  ops ("No ACL grant authorizes the public access block's ops", above): by
  identity and bucket policy, and where no policy decides by the bucket's
  owner alone, within its permission mask, every ACL grantee refused with
  403 AccessDenied. And each retry of a write that lost a race is
  authorized again and stops when the name names another bucket, as
  rgw-go's other retried bucket writes do ("A retried bucket write stops
  when the name names another bucket" and "A PutBucketAcl that loses a race
  is retried, and every retried ACL, policy or tagging write is authorized
  again", below); a retried BucketOwnerEnforced holds the ACL that retry
  read to the default of the owner that ACL names, where v21.1.2 holds it
  to the default of the owner its request loaded
  ([rgw_op.cc:10272-10300](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10272-L10300)). The two
  defaults differ only when the bucket's ACL changed owner between the
  request's load and the retry. The S3 handler does not route the three ops
  yet, so each still answers 501 NotImplemented ("ownershipControls
  requests are served as v21 serves them", above). Nor does this tree
  enforce the setting in full: under BucketOwnerEnforced an object's ACL
  still grants, and PutObject, CopyObject and CreateMultipartUpload still
  take any canned ACL or grant header. Enforcement is complete only once
  both are enforced, and the three ops stay unrouted until then.
- **Deletes that need MFA are refused.** rgw-go checks `x-amz-mfa` where
  radosgw does, with the differences the preflight and reused-code
  exclusions record, but its DeleteObject and DeleteObjects do not take
  the verdict. On a bucket with MFA delete enabled, radosgw refuses a
  DeleteObject that names a version with 403 AccessDenied unless the
  request's MFA token verified ([rgw_op.cc:5158-5163](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5158-L5163),
  [rgw_op.cc:5548-5553](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5548-L5553)), and ceph main refuses a
  DeleteObjects any of whose keys names one likewise. rgw-go refuses each
  of them, whatever the header holds, an admin's included: radosgw checks
  the delete permission first, and when it refuses an admin its override
  lets the delete through without the MFA check
  ([rgw_process.cc:228-236](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L228-L236),
  [rgw_process.cc:228-239](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L228-L239)). On both releases radosgw's
  DeleteObjects check is inverted: it asks for MFA when a key names no
  version, and lets a request whose every key names a version through
  without it ([rgw_op.cc:7067-7081](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7067-L7081),
  [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983); `docs/ceph-upstream-bugs.md`,
  "[radosgw's DeleteObjects asks for MFA for the keys that need none and
  not for the versions that
  do](ceph-upstream-bugs.md#radosgws-deleteobjects-asks-for-mfa-for-the-keys-that-need-none-and-not-for-the-versions-that-do)").
  rgw-go checks as ceph main does, and as v19.2.6 did, on both releases,
  so on a zone of either release it refuses a request naming a version
  that radosgw serves, and serves one naming none that radosgw refuses
  without MFA.
- **Only the bucket owner's x-amz-mfa satisfies MFA delete.** radosgw
  checks a request's `x-amz-mfa` against the requester's own devices, and
  PutBucketVersioning's change of MFA delete, or of the versioning of a
  bucket with it on, takes that verdict from any requester the bucket's
  permissions let act
  ([rgw_op.cc:2795-2817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2795-L2817),
  [rgw_op.cc:3027-3049](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3027-L3049);
  `docs/ceph-upstream-bugs.md`, "[radosgw lets a requester's own MFA device
  satisfy another owner's MFA
  delete](ceph-upstream-bugs.md#radosgw-lets-a-requesters-own-mfa-device-satisfy-another-owners-mfa-delete)").
  The S3 User Guide's "[Configuring MFA
  delete](https://docs.aws.amazon.com/AmazonS3/latest/userguide/MultiFactorAuthenticationDelete.html)"
  (read 2026-10-22): "only the bucket owner (root account) can enable MFA
  delete", and "the bucket owner must include two forms of authentication
  in any request to delete a version or change the versioning state".
  rgw-go counts a verified code there, on both releases, only from the
  bucket's owner (`op.OwnerMFAVerified`): for a bucket a user owns, that
  user, and for a bucket an account owns, the account's root user alone.
  The user record whose device verified the code decides, so a system
  request whose `rgwx-uid` names the owner does not count the system
  user's code. Any other requester's change, an account's other users'
  included, is 403 AccessDenied, as an unverified one is, where radosgw
  serves it. The deletes take no verdict ("Deletes that need MFA are
  refused", above).
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
- **The account root's pass on the bucket policy ops.** On Tentacle,
  radosgw lets the root user of the account that owns a bucket through
  PutBucketPolicy, GetBucketPolicy and DeleteBucketPolicy before any policy
  is evaluated, unless the bucket carries the
  `user.rgw.iam-policy-remove-self-access` attr that a PutBucketPolicy with
  `x-amz-confirm-remove-self-bucket-access` sets (`rgw_op.cc:8983-8986`,
  `:9069-9072`, `:9126-9129` and `:9039-9043` at v20.2.4). rgw-go
  authorizes these ops as Squid does, on both releases, and ignores the
  header: a root whose bucket policy denies it these actions is refused
  where Tentacle lets it through, and no request is let through that Squid
  would refuse. Tentacle never stores the attr, as the system-object write
  skips an empty attr (`docs/ceph-upstream-bugs.md`, "[Tentacle never
  stores the confirmation of
  x-amz-confirm-remove-self-bucket-access](ceph-upstream-bugs.md#tentacle-never-stores-the-confirmation-of-x-amz-confirm-remove-self-bucket-access)"),
  so its pass holds whatever the header said once its caches drop the
  bucket. rgw-go's DeleteBucketPolicy removes the attr on Tentacle, as
  radosgw's does (`:9151-9157` at v20.2.4).
- **A POST policy needs no condition for a checksum field.** Tentacle's
  radosgw lets a POST upload's form carry a checksum field that no
  condition of its policy names: `match_policy_vars` skips
  `x-amz-checksum-algorithm` and `x-amz-checksum-` followed by an
  algorithm's name
  ([rgw_policy_s3.cc:119-121](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L119-L121),
  [rgw_cksum.h:318-334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L318-L334)).
  Squid's `match_policy_vars` makes no such exception
  ([rgw_policy_s3.cc:105-121](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L105-L121)),
  so by its source it answers s3-tests' `test_post_object_upload_checksum`
  403 AccessDenied, "Policy missing condition: x-amz-checksum-sha256".
  rgw-go makes Tentacle's exception on both releases, since it serves
  single-object checksums on Squid too (`postpolicy.Policy.Check`). It
  names the algorithms as `cksum.ParseName` does, so it also excepts
  `xxhash3`, main's name for Tentacle's `xxh3`, which the write then
  refuses, where Tentacle needs a condition for it; main's `md5`,
  `xxhash64` and `xxhash128` need a condition, as on Tentacle. It needs
  the prefix at the start of the name and the whole rest to be the
  algorithm's name, where Tentacle finds the prefix anywhere in the name
  and reads what follows its first fifteen bytes as a C string: a field
  whose name has a NUL byte after the algorithm's, which Tentacle excepts,
  needs a condition in rgw-go (docs/ceph-upstream-bugs.md, "[Tentacle's
  POST policy excepts a field whose name holds a NUL after a checksum
  algorithm's](ceph-upstream-bugs.md#tentacles-post-policy-excepts-a-field-whose-name-holds-a-nul-after-a-checksum-algorithms)").
  Both match the prefixes in lower case only.
- **A POST policy is read as RFC 8259 JSON.** radosgw reads a POST
  upload's policy document with json_spirit's grammar
  ([json_spirit_reader_template.h:462-519](https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L462-L519),
  the same at v20.2.4), which also takes a stray comma in an array or an
  object, C white space such as a form feed, raw control bytes and bytes
  that are not UTF-8 in a string, `\x` and octal escapes, a `\u` escape
  whose digits are not hex, `hex_to_num` reading each such digit as 0
  ([json_spirit_reader_template.h:52-58](https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L52-L58),
  [json_spirit_reader_template.h:73-93](https://github.com/ceph/ceph/blob/v19.2.7/src/json_spirit/json_spirit_reader_template.h#L73-L93),
  the same at v20.2.4), escapes it drops, and numbers with a plus sign,
  leading zeros, or a dot at either end. rgw-go
  reads the document with Go's grammar and answers each of these 400
  InvalidArgument, "Malformed JSON" (`postpolicy.Parse`). It answers so as
  well a string that is not UTF-8, which Go's decoder would repair; an
  escaped UTF-16 surrogate that is not half of a pair, which json_spirit
  writes as three bytes that are no character; arrays and objects nested
  deeper than 10000, where json_spirit recurses without limit
  (docs/ceph-upstream-bugs.md, "[radosgw's JSON parser recurses without
  limit on a POST policy's
  nesting](ceph-upstream-bugs.md#radosgws-json-parser-recurses-without-limit-on-a-post-policys-nesting)");
  and a
  number whose value json_spirit takes from the host's libm: an exponent
  past 300, a fraction of more than 300 digits, the exponent 126, or a
  value that is not finite, which it writes as `inf` or `nan`. An escaped
  surrogate pair is the one character it encodes, as RFC 8259 reads it,
  where json_spirit writes each half alone, so a condition naming a
  character past the Basic Multilingual Plane by its escapes matches the
  character's UTF-8 in a field, where in radosgw only the six bytes
  json_spirit writes match it. Otherwise rgw-go reads the
  document as JSONParser does: the first of duplicate names, an object
  condition's member of the smallest name, the members of an object
  `conditions` in name order, any text after the document ignored, and
  each value as `get_data` gives it, a number as json_spirit writes it
  back, so `1e3` is `1000.0000000000000` and `0.3` is
  `0.30000000000000004`.
- **A POST policy whose expiration is before 1970 has expired.** radosgw
  holds a POST policy's expiration as unsigned seconds,
  `uint64_t expires`
  ([rgw_policy_s3.h:31](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.h#L31),
  the same at v20.2.4), so the negative seconds `internal_timegm` gives a
  date before 1970 wrap past every time when `set_expires` stores them
  ([rgw_policy_s3.cc:132-141](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L132-L141),
  [rgw_policy_s3.cc:141-150](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L141-L150)),
  and the `expires <= now` of `RGWPolicy::check` never finds the policy
  expired
  ([rgw_policy_s3.cc:189-196](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_policy_s3.cc#L189-L196),
  [rgw_policy_s3.cc:198-205](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_policy_s3.cc#L198-L205)).
  rgw-go answers such a policy 403 AccessDenied, "Policy expired"
  (docs/ceph-upstream-bugs.md, "[radosgw's POST policy never expires when
  its expiration is before
  1970](ceph-upstream-bugs.md#radosgws-post-policy-never-expires-when-its-expiration-is-before-1970)").
- **POST policy names are compared over all their bytes.** radosgw
  matches a POST form's field names, and the names a policy gives them,
  in any ASCII case with strcasecmp on C strings, `ltstr_nocase`
  ([rgw_string.h:15-21](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_string.h#L15-L21),
  the same at v20.2.4), which end at a NUL byte, so a field named `key`
  followed by a NUL and more is the field `key`. rgw-go compares the
  whole name (`postpolicy.Env`), so such a field is one of its own, which
  a condition must name.

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
- **CreateBucket answers 409 whenever another owner holds the name.**
  When another owner's create of the same name lands while a CreateBucket
  runs, radosgw answers 200 to the loser for a bucket it does not own
  (`docs/ceph-upstream-bugs.md`, "[radosgw answers 200 to a CreateBucket
  that loses a race to another
  owner](ceph-upstream-bugs.md#radosgw-answers-200-to-a-createbucket-that-loses-a-race-to-another-owner)").
  rgw-go answers 409 BucketAlreadyExists, as both answer when the bucket
  existed before the request. An owner's re-create of its own bucket is 200
  from both.
- **An abandoned bucket-creation try is removed.** When a CreateBucket
  finds its name taken and then finds no bucket under it, a concurrent
  delete having run, radosgw tries again with a new bucket id and leaves
  the abandoned try's instance and index shards in RADOS; it also carries
  that try's in-index log generation into the next, so the bucket it
  creates lists generation 0 once per try (`docs/ceph-upstream-bugs.md`,
  "[radosgw's bucket creation retry leaks the abandoned instance and repeats
  its log
  layout](ceph-upstream-bugs.md#radosgws-bucket-creation-retry-leaks-the-abandoned-instance-and-repeats-its-log-layout)").
  rgw-go removes the abandoned try's instance and index, logging what it
  cannot remove, and starts each try from a fresh layout.
- **A failed owner link fails the create.** When adding a new bucket to its
  owner's bucket list fails, both unlink it again; radosgw then answers 200,
  rgw-go the link's error (`docs/ceph-upstream-bugs.md`, "[radosgw reports a
  bucket whose owner link failed as
  created](ceph-upstream-bugs.md#radosgw-reports-a-bucket-whose-owner-link-failed-as-created)").
  The bucket stays for its owner's re-create, which links it, on both.
- **A bucket delete retries an instance removal that lost to another
  write.** Both gateways read the instance again by its id when a delete
  begins (`RadosBucket::remove`, `driver/rados/rgw_sal_rados.cc:356-360` at
  v19.2.6, `:373-377` at v20.2.4) and remove it under the version read.
  When another write, such as an ACL change or a reshard, changed the
  instance since, radosgw answers 204 with the entry point removed and the
  instance, the index and the owner's list entry left behind
  (`docs/ceph-upstream-bugs.md`, "[radosgw's bucket delete answers success
  when its instance removal loses a
  race](ceph-upstream-bugs.md#radosgws-bucket-delete-answers-success-when-its-instance-removal-loses-a-race)").
  rgw-go reads the instance again from RADOS, past its metadata cache, and
  retries the removal, up to twenty times, then removes the shards of the
  index generation its last read named and the owner's entry. Past twenty
  it answers 500 InternalError and leaves what radosgw's 204 leaves: the
  entry point removed, and the instance, the current generation's index
  shards and the owner's entry in place. Neither removes the shards of an
  earlier generation a reshard left behind. A delete that loses the race
  for the entry point itself leaves everything in place on both, answered
  204.
- **An owner's entry naming another instance stays.** radosgw unlinks a
  bucket from its owner's list by the bucket's name, whatever instance the
  entry names: a delete that found its entry point re-pointed at a bucket
  re-created under the name removes the new bucket's entry
  (`docs/ceph-upstream-bugs.md`, "[radosgw's bucket delete unlinks a bucket
  re-created under the same name from its
  owner](ceph-upstream-bugs.md#radosgws-bucket-delete-unlinks-a-bucket-re-created-under-the-same-name-from-its-owner)").
  Every unlink rgw-go makes, after a delete, after a failed link and after
  a create whose entry point vanished, removes the entry only while it
  names that instance, in an op that compares the entry first; an owner's
  object removed in between counts as unlinked. An entry that changes on
  each of five tries, as concurrent links and stats syncs could make it,
  is left with a warning where radosgw would have removed it. Such an
  entry grants nothing, since authorization reads the entry point and the
  instance, never the owner's list, but ListBuckets shows the name, which
  answers NoSuchBucket, and `max_buckets` counts it. radosgw's stats sync
  does not remove it, as it skips a bucket it cannot load
  (`rgw_sync_all_stats`, `rgw_user.cc:32-38` at both tags); only the
  owner's re-create of the name, whose link points the entry at the new
  bucket, or an admin unlink repairs it. A create whose entry point names another instance once its link is
  written found a bucket created under the name after its own was deleted.
  radosgw unlinks only for an entry point that is gone
  (`rgw_sal_rados.cc:210-227` at v19.2.6, `:227-244` at v20.2.4). When the
  newer bucket is another owner's, rgw-go unlinks its own, now stale,
  entry, which radosgw keeps. When it is the same owner's, its link may
  have pointed the shared entry back at the deleted instance, so rgw-go
  links the newer bucket again and the list names the live bucket, as it
  ends up in radosgw. A delete of that newer bucket that completes between
  the entry-point read and that link leaves the list with an entry for the
  deleted bucket, an entry of the kind above. A failed unlink fails
  rgw-go's delete with its error;
  radosgw logs it and answers 204 (`RGWBucketCtl::do_unlink_bucket`,
  `driver/rados/rgw_bucket.cc:3424-3432` at v19.2.6, and the same code
  inline in `RGWBucketCtl::unlink_bucket`, `:3506-3518` at v20.2.4).
- **An indexless bucket is deleted as each release deletes it.** rgw-go
  creates and removes an indexless bucket's shard objects on Squid and
  none on Tentacle, as each release's radosgw does, and checks a bucket's
  emptiness by listing its shards on both. On Tentacle the delete of an
  indexless bucket created there therefore answers 404 NoSuchKey from both
  gateways and the bucket stays, and the shards of one Squid created stay
  behind after its delete (`docs/ceph-upstream-bugs.md`, "[Tentacle cannot
  delete an indexless bucket it
  created](ceph-upstream-bugs.md#tentacle-cannot-delete-an-indexless-bucket-it-created)").
  rgw-go keeps the check, since skipping it could orphan objects.
- **A bucket delete aborts its multipart uploads under their completion
  lock.** For a bucket of its own zonegroup, after the emptiness check,
  radosgw aborts every upload the bucket's multipart listing names
  (`RadosBucket::abort_multiparts`, `driver/rados/rgw_sal_rados.cc:958-1013`
  at v19.2.6, `:978-1033` at v20.2.4, called at `:395-400` and
  `:412-417`), and so does rgw-go, with two differences:
  - rgw-go aborts each upload as AbortMultipartUpload does, under its
    `RGWCompleteMultipart` lock ("AbortMultipartUpload removes the upload
    before it frees the parts"); `abort_multiparts` calls the abort without
    taking it. An upload whose lock another gateway holds, a completion in
    progress or one that stopped, fails the delete with 503
    ServiceUnavailable until the lock is released or lapses, and so does
    one this gateway's own completion holds, since the abort locks under a
    cookie of its own; the bucket stays. radosgw deletes the bucket and frees
    the upload's parts under the completion.
  - rgw-go aborts each listed upload once. radosgw's listing appends each
    page to the uploads of the pages before, so every later page aborts
    those again and its WARNING counts them again
    (`docs/ceph-upstream-bugs.md`, "[radosgw's bucket delete aborts each page of multipart uploads again on every later page](ceph-upstream-bugs.md#radosgws-bucket-delete-aborts-each-page-of-multipart-uploads-again-on-every-later-page)").
    rgw-go logs the number of uploads it listed, those already gone
    included, once.
  - An upload whose parts the key's object names ("AbortMultipartUpload
    leaves a completed upload's parts alone") is skipped with a warning and
    counted, as radosgw counts the abort it makes of it; the delete goes on
    and leaves its meta object and parts. The emptiness check lets the
    delete reach it only for a head without a visible index entry.
- **A bucket delete removes the lifecycle entry once the bucket is gone.**
  For a bucket whose attrs carry `user.rgw.lc`, both gateways remove its
  entry from its lifecycle shard after the multipart abort, and neither
  fails the delete when that removal fails
  ([rgw_sal_rados.cc:402-407](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L402-L407),
  [rgw_sal_rados.cc:419-424](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L419-L424)).
  radosgw removes it before `delete_bucket`, whose second emptiness check
  or entry point removal can still fail and leave the bucket with its
  lifecycle configuration and without its entry, so its rules never run
  again (`docs/ceph-upstream-bugs.md`, "[radosgw's bucket delete drops the
  lifecycle entry of a bucket it then fails to
  remove](ceph-upstream-bugs.md#radosgws-bucket-delete-drops-the-lifecycle-entry-of-a-bucket-it-then-fails-to-remove)").
  rgw-go removes it as the delete's last step, after the owner's list
  entry, reading the attr from the instance it removed, so a delete that
  fails before, as one a live rename refuses or one that loses its entry
  point to another write does, keeps the entry. The removal waits for the
  shard's lock up to 50 s, as radosgw's does, and a delete whose request
  ends in that wait, or whose entry removal fails, has already removed
  the bucket. The entry it leaves names a bucket the name no longer
  loads, or loads with another marker, which radosgw's next lifecycle
  pass on the shard drops, as `bucket_lc_process` answers -ENOENT for it
  ([rgw_lc.cc:1697-1718](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1697-L1718),
  [rgw_lc.cc:2430-2440](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2430-L2440),
  [rgw_lc.cc:1753-1774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1753-L1774),
  [rgw_lc.cc:2497-2506](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2497-L2506)).
  rgw-go's lifecycle worker drops it in both cases too (`lcLoadBucket`,
  `internal/driver/lc_worker.go`), so a zone of rgw-go gateways alone
  drops it as long as one of them runs its lifecycle threads. It drops an
  entry whose key does not split into a tenant, a name and a marker as
  well, which radosgw's `bucket_lc_process` indexes past
  (docs/ceph-upstream-bugs.md, "[radosgw's bucket_lc_process indexes past a lifecycle entry key of fewer than three parts](ceph-upstream-bugs.md#radosgws-bucket_lc_process-indexes-past-a-lifecycle-entry-key-of-fewer-than-three-parts)").
- **A retried bucket write stops when the name names another bucket.**
  PutBucketPolicy, DeleteBucketPolicy, PutBucketTagging,
  DeleteBucketTagging, PutBucketWebsite, DeleteBucketWebsite,
  PutPublicAccessBlock, DeletePublicAccessBlock,
  PutBucketEncryption, DeleteBucketEncryption, PutBucketCors,
  DeleteBucketCors and PutObjectLockConfiguration retry a write that lost
  a race up to fifteen times, each
  after reading the bucket
  again by name (`retry_raced_bucket_write`, `rgw_op.h:183-196` at v19.2.6,
  `:197-210` at v20.2.4), and so does rgw-go's PutBucketAcl ("A PutBucketAcl
  that loses a race is retried", below). v21.1.2's
  PutBucketOwnershipControls and DeleteBucketOwnershipControls, which
  neither floor has, retry the same way, each through its own
  `retry_raced_bucket_write`
  ([rgw_op.cc:10277-10304](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10277-L10304),
  [rgw_op.cc:10353-10357](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10353-L10357)). The re-read drops the bucket's
  instance id
  ([rgw_rados.cc:9212-9213](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9212-L9213),
  [rgw_rados.cc:9961-9962](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9961-L9962)),
  and the website writes then set or clear the website of whatever bucket
  it found
  ([rgw_op.cc:2924-2929](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2924-L2929)
  and
  [rgw_op.cc:2970-2975](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2970-L2975),
  [rgw_op.cc:3156-3161](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3156-L3161)
  and
  [rgw_op.cc:3202-3207](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3202-L3207)),
  as PutPublicAccessBlock merges into whatever bucket it found the attrs
  its request started with, the old bucket's ACL, policy and tags among
  them, and DeletePublicAccessBlock removes that bucket's block
  ([rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673)
  and
  [rgw_op.cc:8749-8754](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8749-L8754),
  [rgw_op.cc:9637-9641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641)
  and
  [rgw_op.cc:9717-9722](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9717-L9722)),
  and the encryption writes set or remove the default encryption of that
  bucket
  ([rgw_op.cc:8807-8811](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8807-L8811)
  and
  [rgw_op.cc:8860-8866](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8860-L8866),
  [rgw_op.cc:9775-9779](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9775-L9779)
  and
  [rgw_op.cc:9828-9834](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9828-L9834)),
  and the CORS writes set or remove the CORS configuration of that bucket
  ([rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107)
  and
  [rgw_op.cc:6133-6152](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6133-L6152),
  [rgw_op.cc:6770-6774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774)
  and
  [rgw_op.cc:6800-6819](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6800-L6819)),
  and PutObjectLockConfiguration enables object lock on that bucket, when
  its versioning is enabled, and replaces its configuration
  ([rgw_op.cc:8289-8304](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8289-L8304),
  [rgw_op.cc:9224-9239](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9224-L9239)),
  and v21.1.2's ownership controls writes set or remove the ownership
  setting of that bucket.
  When the bucket
  was deleted and a bucket of the same name created in between, radosgw
  writes into the new bucket, another owner's included
  (`docs/ceph-upstream-bugs.md`, "[radosgw's retried bucket write can land
  in a bucket re-created under the same
  name](ceph-upstream-bugs.md#radosgws-retried-bucket-write-can-land-in-a-bucket-re-created-under-the-same-name)").
  rgw-go reads by name too, and when the name now names another instance it
  writes nothing and answers 409 ConcurrentModification, the answer both
  give once the fifteen retries are spent. Its ownership controls writes
  retry the same way, on both releases.
- **A retried PutBucketPolicy keeps what the write it lost to changed.**
  radosgw's PutBucketPolicy merges the attrs its request loaded into every
  try, so a retry writes back the ACL, tags or public-access block another
  write had just changed (`rgw_op.cc:8102` and `:8110-8115` at v19.2.6,
  `:9028` and `:9036-9046` at v20.2.4; `docs/ceph-upstream-bugs.md`,
  "[radosgw's PutBucketPolicy retry writes back the bucket attrs its
  request started
  with](ceph-upstream-bugs.md#radosgws-putbucketpolicy-retry-writes-back-the-bucket-attrs-its-request-started-with)").
  rgw-go sets only the policy over the bucket as it read it last, as
  radosgw's PutBucketTagging does with its tags, so the other write's
  change stays. radosgw's PutPublicAccessBlock merges the same
  request-start attrs into each try
  ([rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673),
  [rgw_op.cc:9637-9641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641)),
  so its retry writes back the ACL, tags or policy another write had just
  changed; rgw-go's PutPublicAccessBlock likewise sets only the block over
  the bucket as each try read it. radosgw judges BlockPublicPolicy once,
  against the block its request loaded; rgw-go judges it again on each retry against the block
  that try read, a block that does not decode blocking, so a public policy
  whose first write lost to a write that blocks public policies is refused
  with 403 AccessDenied where radosgw stores it. radosgw's PutBucketCors
  merges the attrs its request loaded into every try the same way
  ([rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107) at v19.2.7,
  [rgw_op.cc:6770-6774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774) at v20.2.4),
  so a CORS configuration whose first write lost to a PutBucketAcl puts the
  ACL that write replaced back; rgw-go's sets only the configuration over
  the bucket as each try read it, and the winner's ACL stays.
- **A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again.** radosgw stores a bucket's
  new ACL once, under the version its request loaded, and answers 200 when
  another write of the bucket landed first, with the new ACL not stored
  (`rgw_op.cc:5920-5927` at v19.2.6, `:6582-6589` at v20.2.4;
  `docs/ceph-upstream-bugs.md`, "[radosgw's PutBucketAcl answers success
  when its write loses a
  race](ceph-upstream-bugs.md#radosgws-putbucketacl-answers-success-when-its-write-loses-a-race)").
  rgw-go retries the write as the other bucket writes are retried, up to
  fifteen times after reading the bucket again, and on each try refuses a
  new owner, with 403 AccessDenied "Cannot modify ACL Owner", and a public
  ACL under a block of public ACLs, against the ACL and block that try
  read; once the retries are spent it answers 409 ConcurrentModification.
  So where radosgw answers 200 with the old ACL in place, rgw-go stores the
  new one, refuses it, or answers 409, and a retry never writes back the
  ACL of an owner a concurrent change replaced. PutObjectAcl, which radosgw
  serves through the same `RGWPutACLs::execute` and answers 200 the same way
  (`modify_obj_attrs`, `rgw_op.cc:5916-5919` and `:5925-5927` at v19.2.6,
  `:6578-6581` and `:6587-6589` at v20.2.4), is retried as PutBucketAcl is:
  before each retry rgw-go reads the object's head again, makes both checks
  against the ACL it holds, and authorizes the requester again against that
  head, its ACL and the bucket's policy, as the first try was authorized.
  radosgw never retries either ACL write: on a lost race it answers 200 and
  writes nothing (`rgw_op.cc:5912-5927` at v19.2.6, `:6576-6590` at v20.2.4;
  tracker #16930). The re-authorization guards rgw-go's own retry: when the
  write it lost to revoked the requester's WRITE_ACP, the retry answers 403
  AccessDenied, writes nothing, and leaves the revoking ACL in place, where
  radosgw would have answered 200 with that ACL in place too. A lost race
  whose bucket or object already holds the requested ACL answers 200
  without a write, as the client's intent already holds: the retry that
  finds it has authorized the requester again and made both checks first,
  and compares the stored ACL decoded, the same owner and the same grants
  in any order, never its bytes. An admin is
  let through a retry's unmarked access denial as Run lets one through the
  first check, and through nothing else. Every retried bucket write,
  PutBucketAcl, PutBucketPolicy, DeleteBucketPolicy, PutBucketTagging,
  DeleteBucketTagging, PutBucketWebsite, DeleteBucketWebsite,
  PutPublicAccessBlock, DeletePublicAccessBlock,
  PutBucketEncryption, DeleteBucketEncryption, PutBucketCors,
  DeleteBucketCors, PutObjectLockConfiguration, PutBucketOwnershipControls
  and DeleteBucketOwnershipControls, is authorized again
  the same way before each retry,
  against the bucket that
  retry read: its ACL, of which the block and ownership controls writes read
  the owner alone, its policy and its public-access block, with the same
  admin rule. radosgw retries all but PutBucketAcl through
  `retry_raced_bucket_write` (`rgw_op.cc:1197`, `:1232`, `:8110` and `:8204`
  at v19.2.6, `:1434`, `:1469`, `:9036` and `:9151` at v20.2.4; the website
  writes at
  [rgw_op.cc:2924-2929](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2924-L2929)
  and
  [rgw_op.cc:2970-2975](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2970-L2975),
  [rgw_op.cc:3156-3161](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3156-L3161)
  and
  [rgw_op.cc:3202-3207](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3202-L3207);
  the block writes at
  [rgw_op.cc:8669-8673](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8669-L8673)
  and
  [rgw_op.cc:8749-8754](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8749-L8754),
  [rgw_op.cc:9637-9641](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9637-L9641)
  and
  [rgw_op.cc:9717-9722](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9717-L9722);
  the encryption writes at
  [rgw_op.cc:8807-8811](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8807-L8811)
  and
  [rgw_op.cc:8860-8866](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8860-L8866),
  [rgw_op.cc:9775-9779](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9775-L9779)
  and
  [rgw_op.cc:9828-9834](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9828-L9834);
  the CORS writes at
  [rgw_op.cc:6103-6107](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6103-L6107)
  and
  [rgw_op.cc:6133-6152](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6133-L6152),
  [rgw_op.cc:6770-6774](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6770-L6774)
  and
  [rgw_op.cc:6800-6819](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6800-L6819);
  the object-lock write at
  [rgw_op.cc:8289-8304](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8289-L8304),
  [rgw_op.cc:9224-9239](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9224-L9239);
  and the ownership controls writes, which neither floor has, at v21.1.2's
  [rgw_op.cc:10277-10304](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10277-L10304)
  and
  [rgw_op.cc:10353-10357](https://github.com/ceph/ceph/blob/v21.1.2/src/rgw/rgw_op.cc#L10353-L10357)),
  which reads the bucket again and authorizes nothing, so when the
  write a try lost to revoked the requester's permission, by an ACL that
  drops their grant or a policy that denies them, radosgw's retry writes for
  them anyway, and can replace or remove the very policy that denied them
  (`docs/ceph-upstream-bugs.md`, "[radosgw's retried bucket writes are not
  authorized
  again](ceph-upstream-bugs.md#radosgws-retried-bucket-writes-are-not-authorized-again)").
  rgw-go's retry answers 403 AccessDenied, writes nothing, and leaves the
  revoking ACL or policy in place.
  `test_bucket_concurrent_set_canned_acl`'s fifty concurrent PutBucketAcl
  requests all ask for one ACL, so a request that loses the race finds it
  in place and answers 200, and rgw-go passes the test as radosgw does.
- **PutBucketVersioning answers MalformedXML for a document that does not
  parse.** radosgw's `get_params` answers -EINVAL, 400 InvalidArgument, for
  a body expat refuses and for one whose root is not
  `VersioningConfiguration` ([rgw_rest_s3.cc:2228-2239](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2228-L2239),
  [rgw_rest_s3.cc:2323-2334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2323-L2334)), and for a `Status` other than
  `Enabled` or `Suspended`, a missing one included, or an `MfaDelete` other
  than `Enabled` or `Disabled`, each the element's own text compared
  untrimmed and case for case ([rgw_rest_s3.cc:2179-2210](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2179-L2210),
  [rgw_rest_s3.cc:2274-2305](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2274-L2305)). rgw-go answers each with 400
  MalformedXML, Amazon S3's code for a body that does not parse. S3's
  PutBucketVersioning reference lists `Enabled | Suspended` and `Enabled |
  Disabled` as the only values and names no code for a value outside them,
  so rgw-go answers such a value as it answers a body that does not parse.
  Before it builds a tree of the body, rgw-go also refuses with 400
  MalformedXML these documents, which radosgw reads:
  - one with a document type declaration, whose internal subset can declare
    entities that expat, as `RGWXMLParser` sets it up, expands
    ([rgw_xml.cc:213-223](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L213-L223),
    [rgw_xml.cc:213-223](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L213-L223));
  - one with an element below the root's children: radosgw reads a
    `Status` that holds an element by its own text, and ignores an element
    the schema does not name however deep it nests;
  - one with a second `Status` or `MfaDelete`, where radosgw reads the
    first ([rgw_xml.h:219-241](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L219-L241),
    [rgw_xml.h:219-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L219-L241));
  - one with a prefixed element name, which radosgw, with namespace
    processing off ([rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158),
    [rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L158)), reads as another name, so that it
    refuses such a root and ignores such a child;
  - one declaring a namespace other than S3's,
    `http://s3.amazonaws.com/doc/2006-03-01/`, the empty one included.

  A document without a namespace, an element the schema does not name
  directly under the root, and attributes are read as radosgw reads them.
  S3's reference marks `Status` as not required; both gateways refuse a
  document without one. A body longer than `rgw_max_put_param_size` is 416
  InvalidRange from both.
- **A retried PutBucketVersioning checks object lock and MFA delete
  again.** radosgw makes PutBucketVersioning's object-lock and MFA delete
  checks once, against the bucket its request loaded
  ([rgw_op.cc:2788-2817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2788-L2817),
  [rgw_op.cc:3020-3049](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3020-L3049)), and retries a write that lost a race
  over the bucket it reads again, with the attrs its request loaded put
  back ([rgw_op.cc:2828-2849](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2828-L2849),
  [rgw_op.cc:3060-3081](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3060-L3081)). A suspension that loses to a
  PutObjectLockConfiguration, which enables object lock on a
  versioning-enabled bucket inside its own retry
  ([rgw_op.cc:8289-8304](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8289-L8304),
  [rgw_op.cc:9224-9238](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9224-L9238)), so suspends an object-lock bucket, and
  one that loses to a verified change enabling MFA delete flips the
  versioning state without a verified `x-amz-mfa`
  (`docs/ceph-upstream-bugs.md`, "[radosgw's retried PutBucketVersioning
  skips its object-lock and MFA delete
  checks](ceph-upstream-bugs.md#radosgws-retried-putbucketversioning-skips-its-object-lock-and-mfa-delete-checks)").
  rgw-go makes both checks on every try against the bucket that try read,
  so such a retry answers 409 InvalidBucketState or 403 AccessDenied and
  writes nothing. Each try writes the attrs it read, so the change it lost
  to stays (`docs/ceph-upstream-bugs.md`, "[radosgw's PutBucketPolicy retry
  writes back the bucket attrs its request started
  with](ceph-upstream-bugs.md#radosgws-putbucketpolicy-retry-writes-back-the-bucket-attrs-its-request-started-with)"),
  and each retry is authorized again and stops when the name names another
  bucket, as rgw-go's other retried bucket writes do ("A PutBucketAcl that
  loses a race is retried, and every retried ACL, policy or tagging write
  is authorized again" and "A retried bucket write stops when the name
  names another bucket", above). A verified request sets the MFA delete
  flag to its `MfaDelete` on every try, where radosgw sets it only when it
  differed from the flag its request loaded; the two differ only when
  another request changed the flag in between.
- **PutBucketWebsite answers MalformedXML for a document that does not
  parse or fit S3's schema.** radosgw's `get_params` answers -EINVAL, 400
  InvalidArgument, for a body expat refuses and for one whose root is not
  `WebsiteConfiguration`
  ([rgw_rest_s3.cc:2300-2310](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2300-L2310),
  [rgw_rest_s3.cc:2395-2405](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2395-L2405)). rgw-go answers both
  with 400 MalformedXML, Amazon S3's code for configuration XML that "doesn't
  conform to the published XSD" (aws-sdk-go v1.55.8,
  `service/s3/api.go:20217-20221`). It holds the document to S3's schema
  as that SDK models it (`service/s3/api.go`: WebsiteConfiguration
  `:43877-43893`, ErrorDocument `:20424-20435`, IndexDocument
  `:27208-27223`, RedirectAllRequestsTo `:39566-39577`, RoutingRule
  `:40688-40703`, Condition `:14366-14388`, Redirect `:39480-39514`), and
  answers MalformedXML too for these documents, which radosgw reads:
  - one with an element the schema does not hold where it stands, which
    radosgw ignores, so that a misspelled `Condition` turns a prefix rule
    into one that redirects every key;
  - one with a second of an element the schema allows once, every one but
    `RoutingRule`, where radosgw reads the first
    (`RGWXMLDecoder::decode_xml`, [rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241),
    [rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L218-L241));
  - one with a field that holds an element, whose own text radosgw reads;
  - one with an attribute other than `xmlns` naming S3's namespace,
    `http://s3.amazonaws.com/doc/2006-03-01/`: another namespace, the empty
    one, a prefix declaration or any other attribute, which radosgw
    ignores;
  - one with text other than XML whitespace (space, tab, carriage return
    and line feed) between elements, which radosgw ignores;
  - one with a prefixed element name, which radosgw, with namespace
    processing off (`XML_ParserCreate`, [rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158),
    [rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L158)), reads as another name, so that it
    ignores an `s3:IndexDocument` even where `s3` names S3's namespace;
  - one with a document type declaration, whose internal subset can declare
    entities that expat, as `RGWXMLParser` sets it up, expands
    ([rgw_xml.cc:213-223](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L213-L223),
    [rgw_xml.cc:213-223](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L213-L223));
  - one where `RedirectAllRequestsTo` stands beside another property, which
    S3's model forbids (`:43886-43889`) and radosgw ignores
    (`RGWBucketWebsiteConf::decode_xml`, [rgw_website.cc:323-341](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L323-L341),
    [rgw_website.cc:323-341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L323-L341));
  - one with an `ErrorDocument` without a `Key`, or with an empty one,
    which the model requires one byte long at least (`:20427-20434`) and
    radosgw stores as no error document;
  - one whose `Suffix` holds a slash, which the model forbids
    (`:27211-27222`);
  - one with a `RoutingRule` without a `Redirect`, which the model requires
    (`:40697-40702`) and radosgw answers with a redirect to the request's
    own key on the request's own host;
  - one with a `Protocol`, in `RedirectAllRequestsTo` or in a rule's
    `Redirect`, other than exactly `http` or `https`, the enumeration S3's
    model gives it (`:39492`, `:39576` and `:45615-45619`). radosgw stores
    any text and writes it into the redirect's Location
    (`RGWBWRoutingRule::apply_rule`,
    [rgw_website.cc:40-63](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L40-L63),
    [rgw_website.cc:40-63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L40-L63));
  - one longer than 1 MiB, `rgw_max_put_param_size`'s default
    ([rgw.yaml.in:138-145](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145),
    [rgw.yaml.in:144-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151)), the limit `get_params` reads
    the body under ([rgw_rest_s3.cc:2281-2289](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2281-L2289),
    [rgw_rest_s3.cc:2376-2384](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2376-L2384)), which radosgw parses when an
    operator raises the option.

  rgw-go answers every MalformedXML before any InvalidArgument, so its
  answer does not depend on where in the document each sits; radosgw
  answers the first refusal its decode reaches. What is left is radosgw's:
  a missing redirect-all `HostName`, an `HttpRedirectCode` outside 301-399
  or an `HttpErrorCodeReturnedEquals` outside 400-599, each read as
  `strtol` reads a decimal, and `ReplaceKeyPrefixWith` beside
  `ReplaceKeyWith` are 400 InvalidArgument with no message, as radosgw's
  decode refusals are; an empty redirect-all `HostName`, no
  `IndexDocument` and an absent or empty `Suffix` are 400 InvalidArgument
  with radosgw's messages; and more rules than
  `rgw_website_routing_rules_max_num` is 400 InvalidRequest with its
  message ([rgw_rest_s3.cc:2312-2342](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2312-L2342),
  [rgw_rest_s3.cc:2407-2437](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2407-L2437)). S3's model requires the
  redirect-all `HostName` and the `Suffix` too; for those rgw-go keeps
  radosgw's InvalidArgument, the refusal radosgw already makes. rgw-go adds
  one InvalidArgument, with no message, that radosgw does not make: a
  `HostName`, in `RedirectAllRequestsTo` or a rule's `Redirect`, holding a
  byte at or below 0x20, or 0x7F, which radosgw stores and writes raw into
  the Location ("A website redirect is refused for a host holding a control
  byte or a protocol other than http or https", below).
  Three empty elements are accepted where S3's model asks for content. An
  empty `Condition` is no condition, though the model asks a `Condition` to
  hold one of its fields (`service/s3/api.go:14369-14387`). An empty
  `Redirect` sends the request to its own host and key, as radosgw reads
  it, though the model marks `HttpRedirectCode`, `ReplaceKeyPrefixWith` and
  `ReplaceKeyWith` as not required only when a sibling is present
  (`service/s3/api.go:39480-39514`). An empty `RoutingRules` is no rules,
  so it may stand beside `RedirectAllRequestsTo`, which the model otherwise
  wants alone. radosgw's GetBucketWebsite writes a rule with no condition
  as an empty `Condition`, and a rule's `Redirect` even when it is empty
  (`RGWBWRoutingRule::dump_xml`,
  [rgw_website.cc:277-281](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L277-L281),
  [rgw_website.cc:277-281](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L277-L281)); botocore 1.43.90 writes
  `<RoutingRules />` for an empty list and `<Redirect />` for an empty
  redirect; and aws-sdk-go-v2's s3 v1.114.2 writes `RoutingRules` whenever
  its list is not nil (`serializers.go:16017-16027`). So a configuration
  read from radosgw, or one an SDK writes for an empty list or redirect, is
  not refused for its empty elements. An empty element that holds an
  unknown element or text is still refused. The strict reading does refuse
  a configuration radosgw stored with a value outside the schema, such as
  a `Protocol` of `ftp` or a `Suffix` holding a slash: radosgw renders it
  on GET, and putting it back to rgw-go is MalformedXML.
  `ParseXML` (`internal/website/xml.go`, its scan in
  `internal/website/scan.go`) holds these rules
  (`docs/ceph-upstream-bugs.md`, "[radosgw's PutBucketWebsite answers
  InvalidArgument for a document that does not
  parse](ceph-upstream-bugs.md#radosgws-putbucketwebsite-answers-invalidargument-for-a-document-that-does-not-parse)").
- **PutBucketLifecycleConfiguration refuses a document whose root is not
  LifecycleConfiguration.** radosgw's `RGWPutLC::execute` looks the root
  up as an optional field
  ([rgw_op.cc:5996-6002](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5996-L6002),
  [rgw_op.cc:6662-6668](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6662-L6668);
  `RGWXMLDecoder::decode_xml`,
  [rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L218-L241),
  [rgw_xml.h:218-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L218-L241)),
  so a root of another name, or a prefixed one, which expat without
  namespace processing, `XML_ParserCreate(nullptr)`, reads as another name
  ([rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L158),
  [rgw_xml.cc:158](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L158)),
  is read as the empty configuration, which it stores over the bucket's
  rules and answers 200 (docs/ceph-upstream-bugs.md, "[radosgw stores an
  empty lifecycle configuration for a body whose root is not
  LifecycleConfiguration](ceph-upstream-bugs.md#radosgw-stores-an-empty-lifecycle-configuration-for-a-body-whose-root-is-not-lifecycleconfiguration)").
  S3's model takes the body as a `LifecycleConfiguration` in S3's
  namespace holding a required list of rules, and S3 answers MalformedXML
  to configuration XML "that doesn't conform to the published XSD" (the
  AWS SDK for Go's model,
  [api.go:34969](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L34969),
  [api.go:13051-13057](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L13051-L13057),
  [api.go:20217-20221](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20217-L20221)).
  rgw-go answers 400 MalformedXML (`lifecycle.Parse`,
  `internal/lifecycle/xml.go`) to a root other than an unprefixed
  `LifecycleConfiguration`, a prefixed one included even when its prefix
  is bound to S3's namespace, as its other S3 documents do. It does not
  check the root's namespace declaration, which radosgw ignores too. A
  correct client's document, under the right root, is answered alike by
  both. This is the correct S3 answer that the design's compatibility rule
  asks for
  (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`).
  AWS's answer was not checked against Amazon S3 itself. The empty
  configuration radosgw stores for such a body is read as none
  ("GetBucketLifecycleConfiguration answers a stored configuration without
  rules with 404", below).
- **GetBucketLifecycleConfiguration answers a stored configuration without
  rules with 404.** radosgw's GetBucketLifecycleConfiguration renders
  whatever `user.rgw.lc` decodes to, so the empty configuration its PUT
  stores for a body whose root is not LifecycleConfiguration (the entry
  above) answers 200 with a `LifecycleConfiguration` holding no `Rule`
  ([rgw_rest_s3.cc:3688-3726](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3688-L3726),
  [rgw_rest_s3.cc:4139-4177](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4139-L4177)).
  S3's model never holds a configuration without a rule, its `Rules`
  required
  ([api.go:13051-13057](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L13051-L13057)).
  As the design's compatibility rule asks, reading
  correctly what radosgw's defect leaves
  (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`),
  rgw-go answers such an attr with radosgw's 404
  NoSuchLifecycleConfiguration and no message, the answer for a bucket
  with no configuration (`GetBucketLifecycle.Execute`,
  `internal/op/lifecycle.go`). The attr is left as stored, and its lc entry
  too; radosgw's worker finds no rule in it. An attr that does not decode
  is 500 UnknownError from both.
- **PutBucketLifecycleConfiguration holds a rule's numbers to S3's
  types.** radosgw keeps every number of a lifecycle rule as its
  element's text (`decode_xml_obj(std::string&)`,
  [rgw_xml.h:179-182](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.h#L179-L182),
  [rgw_xml.h:179-182](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.h#L179-L182))
  and reads it only where it uses it: a day count with `atoi`
  ([rgw_lc.h:76](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L76),
  [rgw_lc.h:76](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L76)),
  an object size with `std::stoull`
  ([rgw_lc.h:264-278](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L264-L278),
  [rgw_lc.h:264-278](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L264-L278)).
  `LCFilter_S3::decode_xml` refuses a filter whose `ObjectSizeLessThan` is
  at or below its `ObjectSizeGreaterThan` by comparing the two texts
  bytewise
  ([rgw_lc_s3.cc:169-174](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L169-L174),
  [rgw_lc_s3.cc:169-174](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc_s3.cc#L169-L174)).
  So radosgw stores Days of `5x` or ` 7`, answers InvalidArgument to
  `thirty`, which atoi reads as 0, refuses ObjectSizeGreaterThan 9 with
  ObjectSizeLessThan 10 and takes 10 with 9 (docs/ceph-upstream-bugs.md,
  "[radosgw's lifecycle grammar reads its numbers as
  text](ceph-upstream-bugs.md#radosgws-lifecycle-grammar-reads-its-numbers-as-text)").
  S3's model types the day counts and NewerNoncurrentVersions as integers
  and the object sizes as longs (the AWS SDK for Go's model:
  DaysAfterInitiation,
  [api.go:12222](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L12222);
  Expiration's Days,
  [api.go:28328](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L28328);
  the sizes in And and in the Filter,
  [api.go:28530-28533](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L28530-L28533),
  [api.go:28618-28621](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L28618-L28621);
  NoncurrentVersionExpiration's,
  [api.go:32242-32249](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L32242-L32249);
  NoncurrentVersionTransition's NoncurrentDays,
  [api.go:32305](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L32305);
  Transition's Days,
  [api.go:42703](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L42703)),
  and S3 answers MalformedXML to configuration XML that does not conform
  to its schema
  ([api.go:20217-20221](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L20217-L20221)).
  As the design's compatibility rule asks
  (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`),
  rgw-go (`lifecycle.Parse`, `internal/lifecycle/xml.go`):
  - reads Expiration's Days, Transition's Days, the NoncurrentDays of
    NoncurrentVersionExpiration and of NoncurrentVersionTransition,
    NoncurrentVersionExpiration's NewerNoncurrentVersions and
    DaysAfterInitiation as an xs:int, and ObjectSizeGreaterThan and
    ObjectSizeLessThan as an xs:long: an optional sign, then decimal
    digits, XML whitespace around them collapsed, within the type's range.
    Anything else, an empty element included, is 400 MalformedXML, where
    radosgw stores the text, or answers InvalidArgument to one atoi reads
    as 0 or less;
  - compares the two sizes as numbers, and answers MalformedXML, radosgw's
    code for that refusal, when ObjectSizeLessThan is at or below
    ObjectSizeGreaterThan, so it takes 9 with 10 and refuses 10 with 9;
  - answers 400 InvalidArgument to a negative ObjectSizeGreaterThan or
    ObjectSizeLessThan, once the document has no MalformedXML: S3's sizes
    are byte counts ("Object size values are in bytes", the S3 User
    Guide's "Adding filters to Lifecycle rules"). radosgw stores one, and
    its worker reads it with stoull as 2^64 less its magnitude, so a rule
    whose ObjectSizeLessThan is `-1` applies to every object and one whose
    ObjectSizeGreaterThan is `-1` to none;
  - answers 400 InvalidArgument to a NewerNoncurrentVersions outside 1 to
    100, the range S3 documents ("up to 100",
    [api.go:32236-32242](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L32236-L32242);
    "between 1 and 100", the S3 User Guide's "Lifecycle configuration
    elements"). radosgw stores any count, and its worker holds atoi's int
    in a uint64_t, so a negative count never acts, 0 acts as if no count
    were set, and one above 100 keeps that many versions;
  - stores each number in decimal, without sign or leading zero, where
    radosgw stores the text as written, so GetBucketLifecycleConfiguration
    returns `7` for a Days of ` +007 `. Either gateway's worker reads the
    stored decimal as the client meant it.

  What stays radosgw's: day counts of 0 or less, and the rule's other
  checks, are its InvalidArgument; a NewerNoncurrentVersions inside a
  NoncurrentVersionTransition, which radosgw does not store, is ignored;
  and a NewerNoncurrentVersions in a rule without a Filter is taken, as
  radosgw takes it, where the S3 User Guide states that it needs one, a
  rule the SDK's model does not carry. A configuration radosgw stored with
  other text is written back by GetBucketLifecycleConfiguration as stored.
  The status codes Amazon S3 answers for a negative size and a count
  outside 1 to 100 were not checked against Amazon S3 itself;
  InvalidArgument is radosgw's code for a value out of range.
- **PutBucketLifecycleConfiguration holds a Date and
  ExpiredObjectDeleteMarker to S3's types.** radosgw's `check_date` asks of
  a Date only that `from_iso_8601`'s instant be at midnight UTC
  ([rgw_lc_s3.cc:19-30](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L19-L30),
  [rgw_lc_s3.cc:19-30](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc_s3.cc#L19-L30)).
  `from_iso_8601` stops at white space and carries a field past its range
  into the next
  ([iso_8601.cc:39-151](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L39-L151),
  [iso_8601.cc:39-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.cc#L39-L151)),
  and `from_time_t`, counting u64 nanoseconds, wraps a time past
  2554-07-21T23:34:33.709551615Z into another instant
  ([ceph_time.h:149-151](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_time.h#L149-L151),
  [ceph_time.h:146-148](https://github.com/ceph/ceph/blob/v20.2.4/src/common/ceph_time.h#L146-L148)).
  So radosgw stores a bare date such as `2026-01-01`, a year alone,
  `2026-01-01 later`, `2026-13-01` and `2026-04-31` as written, which
  GetBucketLifecycleConfiguration sends back in a form aws-sdk-go-v2
  cannot read (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle grammar
  reads its numbers as
  text](ceph-upstream-bugs.md#radosgws-lifecycle-grammar-reads-its-numbers-as-text)"),
  and stores `2600-01-01T23:34:33.709551616Z`, whose wrap is
  2015-06-14T00:00:00Z, which its worker treats as long past and acts on at
  once (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle Date wraps past
  2554 and expires at
  once](ceph-upstream-bugs.md#radosgws-lifecycle-date-wraps-past-2554-and-expires-at-once)").
  It sets the delete-marker action, `dm_expiration`, only for the exact
  text `true`
  ([rgw_lc_s3.cc:46-62](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc_s3.cc#L46-L62),
  [rgw_lc_s3.cc:46-62](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc_s3.cc#L46-L62)),
  so a rule whose ExpiredObjectDeleteMarker is `1`, ` true `, `True` or
  `yes` has no delete-marker action: radosgw answers InvalidArgument when
  the rule has no other action and stores it without one otherwise
  (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle grammar reads its
  numbers as
  text](ceph-upstream-bugs.md#radosgws-lifecycle-grammar-reads-its-numbers-as-text)").
  S3's model makes a Date an ISO 8601 timestamp at midnight UTC and
  ExpiredObjectDeleteMarker a boolean (the AWS SDK for Go's model,
  [api.go:28322-28334](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L28322-L28334),
  [api.go:42697-42699](https://github.com/aws/aws-sdk-go/blob/v1.55.8/service/s3/api.go#L42697-L42699)).
  As the design's compatibility rule asks
  (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`),
  rgw-go (`lifecycle.Parse`, `internal/lifecycle/xml.go`):
  - takes the Date of an Expiration or a Transition only when it is
    written as a full date-time, `YYYY-MM-DDThh:mm:ss` with an optional
    fraction and then `Z`, and `from_iso_8601`'s reading of it, with
    nothing but XML whitespace after it, is a calendar date none of whose
    fields is carried, at 00:00:00 UTC, which real_time holds. It answers
    400 MalformedXML to any other: a bare date, a year or a year and month
    alone, and a time to the hour or the minute, which radosgw takes, so a
    client that sends one gets 400 where radosgw stores it; and a missing
    `Z` or a `+00:00` offset, a form S3 may take, which `from_iso_8601`
    cannot read and radosgw refuses too. It stores the Date as written, the
    bytes radosgw would store;
  - reads ExpiredObjectDeleteMarker as an xs:boolean, `true`, `false`, `1`
    or `0` with XML whitespace around it collapsed, `1` setting the action,
    and answers 400 MalformedXML to anything else.

  s3-tests' date tests keep radosgw's answers, as botocore 1.43.90 sends
  every Date as a full date-time: `test_lifecycle_set_date`'s
  `2017-09-27` as `2017-09-27T00:00:00Z`, and
  `test_lifecycle_expiration_date`'s `2015-01-01` and `2030-01-01` as
  `2015-01-01T00:00:00Z` and `2030-01-01T00:00:00Z`, which both take. It
  reads the all-digit `20200101` of
  `test_lifecycle_set_invalid_date` and `20220927` of
  `test_lifecycle_transition_set_invalid_date` as seconds since the epoch,
  so it sends `1970-08-22T19:08:21Z` and `1970-08-23T00:55:27Z`, which
  neither takes, not being at midnight. AWS's answer to each case was not
  checked against Amazon S3 itself.
- **The lifecycle prefix map reads a stored negative object size by its
  sign, and no Date past real_time.** radosgw's lifecycle worker reads a
  stored object size with `std::stoull`
  ([rgw_lc.h:264-278](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.h#L264-L278),
  [rgw_lc.h:264-278](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.h#L264-L278)),
  so a negative size, which its PutBucketLifecycleConfiguration stores, is
  2^64 less its magnitude, and `pass_size_limit_checks`
  ([rgw_lc.cc:516-545](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L516-L545),
  [rgw_lc.cc:524-552](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L524-L552))
  applies a rule whose ObjectSizeLessThan is `-1` to every object and one
  whose ObjectSizeGreaterThan is `-1` to none (docs/ceph-upstream-bugs.md,
  "[radosgw's lifecycle grammar reads its numbers as
  text](ceph-upstream-bugs.md#radosgws-lifecycle-grammar-reads-its-numbers-as-text)").
  It reads a stored Date with `from_iso_8601`, whose real_time wraps a time
  past 2554-07-21T23:34:33.709551615Z into another instant, and acts once
  that instant has passed
  ([rgw_lc.cc:1145-1154](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1145-L1154),
  [rgw_lc.cc:1334-1343](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1334-L1343)
  at v19.2.7;
  [rgw_lc.cc:1165-1174](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1165-L1174),
  [rgw_lc.cc:1356-1365](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1356-L1365)
  at v20.2.4), so a stored `2600-01-01T23:34:33.709551616Z` acts at once
  (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle Date wraps past 2554
  and expires at
  once](ceph-upstream-bugs.md#radosgws-lifecycle-date-wraps-past-2554-and-expires-at-once)").
  As the design's compatibility rule asks, reading correctly whatever
  state the defect leaves
  (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`),
  rgw-go's prefix map (`Configuration.PrefixMap`,
  `internal/lifecycle/prefixmap.go`):
  - keeps a stored size's sign, so `Op.SizeMatches` applies an
    ObjectSizeLessThan of 0 or less to no object and a negative
    ObjectSizeGreaterThan to every object, as the sender meant them, a size
    past int64's range held at its bound;
  - holds no date for a stored Date that real_time cannot hold, so it names
    no instant to act at.

  rgw-go's PutBucketLifecycleConfiguration stores neither (the two entries
  above). What stays radosgw's: day counts read with atoi, a size that is
  not negative read with stoull, a negative NewerNoncurrentVersions, which
  never acts, and one of 0, which acts as if none were set, a Date that
  `from_iso_8601` rolls forward, such as `2026-13-01`, read as the instant
  it rolls to, and a bare date or a year, read as its first midnight.
- **PutBucketLifecycleConfiguration refuses an indexless bucket on Squid
  too.** Tentacle's radosgw answers it 405 MethodNotAllowed, "Indexless
  buckets do not support lifecycle policy", before it reads the
  Content-MD5 or the body
  ([rgw_op.cc:6594-6599](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6594-L6599)).
  Squid's stores the configuration and the bucket's lc entry and answers
  200, though its worker cannot list an indexless bucket, so the rules
  never act
  ([rgw_op.cc:5933-6028](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5933-L6028);
  docs/ceph-upstream-bugs.md, "[Squid stores a lifecycle configuration on
  an indexless bucket, which its worker cannot
  list](ceph-upstream-bugs.md#squid-stores-a-lifecycle-configuration-on-an-indexless-bucket-which-its-worker-cannot-list)").
  rgw-go answers Tentacle's 405 and message on both releases
  (`PutBucketLifecycle.Execute`, `internal/op/lifecycle.go`), and stores
  nothing.
- **PutBucketLifecycleConfiguration answers a Content-MD5 that is not a
  16-byte digest with InvalidDigest, before reading the body.** radosgw
  decodes the header with `from_base64` and answers one that does not
  decode with 400 BadDigest, "Request header Content-MD5 contains character
  that is not base64 encoded."; once it has read the body it compares 16
  bytes of the decoded value with the body's MD5 whatever the value's
  length, and answers a difference with 400 BadDigest, "The Content-MD5 you
  specified did not match what we received."
  ([rgw_op.cc:5945-5988](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5945-L5988),
  [rgw_op.cc:6611-6654](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6611-L6654);
  docs/ceph-upstream-bugs.md, "[radosgw's PutBucketLifecycle compares 16
  bytes of a Content-MD5 of any
  length](ceph-upstream-bugs.md#radosgws-putbucketlifecycle-compares-16-bytes-of-a-content-md5-of-any-length)").
  So radosgw:
  - takes a value longer than 16 bytes whose first 16 are the body's MD5;
  - takes a 15-byte value equal to the MD5's first 15 bytes when the MD5's
    last byte is 0, one body in 256, as the 16th byte it compares is the
    value's terminating NUL, and compares a shorter value's NUL and then
    bytes the string never set;
  - answers a value of the wrong length only after the body checks, so
    with 411 MissingContentLength or 416 InvalidRange beside a body that
    has no length or is over `rgw_max_put_param_size`;
  - ends on a value of '=' alone when it is built with
    `-D_GLIBCXX_ASSERTIONS` (docs/ceph-upstream-bugs.md, "[radosgw's
    from_base64 reads before an all-'=' input](ceph-upstream-bugs.md#radosgws-from_base64-reads-before-an-all--input)").

  S3 answers a Content-MD5 that is not a well-formed digest with 400
  InvalidDigest, and BadDigest only for a well-formed one the body does
  not match: s3-tests, written against AWS, expect InvalidDigest for a
  value that decodes short and for an empty one, and BadDigest for a
  mismatch
  ([test_headers.py:189-207](https://github.com/ceph/ceph/blob/d8552606423c86efe4df2d39c0b7e4b20ba69c73/src/test/rgw/s3-tests/s3tests/functional/test_headers.py#L189-L207)
  at ceph main d8552606). radosgw's own PutObject answers a decoded length
  other than 16 with InvalidDigest
  ([rgw_op.cc:4184-4194](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4184-L4194),
  [rgw_op.cc:4393-4403](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4393-L4403)).
  rgw-go answers a Content-MD5 that does not decode with 400 InvalidDigest
  and radosgw's message for it, and one that decodes to other than 16
  bytes, an empty or all-'=' one included, with 400 InvalidDigest and no
  message, both before it reads the body (`lifecycleContentMD5`,
  `internal/op/lifecycle.go`). A 16-byte one the body does not match is
  answered as radosgw answers it, BadDigest with its message, once the body
  is read.
- **A lifecycle configuration put or delete that loses a race is retried,
  and authorized again.** radosgw's PutBucketLifecycleConfiguration stores
  its configuration, and its DeleteBucketLifecycle erases it, in one write
  under the bucket version its request loaded, so a write of the bucket
  that lands in between fails the request with 409 ConcurrentModification
  ([rgw_lc.cc:2642-2654](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2642-L2654)
  and
  [rgw_lc.cc:2676-2684](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2676-L2684),
  [rgw_lc.cc:2710-2721](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2710-L2721)
  and
  [rgw_lc.cc:2743-2751](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2743-L2751);
  docs/ceph-upstream-bugs.md, "[radosgw's lifecycle configuration put and
  delete fail on a lost bucket race instead of
  retrying](ceph-upstream-bugs.md#radosgws-lifecycle-configuration-put-and-delete-fail-on-a-lost-bucket-race-instead-of-retrying)").
  rgw-go retries each as its other bucket writes are retried, up to
  fifteen times after reading the bucket again by name, and authorizes the
  requester again before each retry against the bucket that retry read, as
  "A PutBucketAcl that loses a race is retried, and every retried ACL,
  policy or tagging write is authorized again" describes; a retry of the
  delete that finds the attr gone writes nothing. A name that now names
  another bucket ends it with 409 and nothing written. The put writes the
  configuration's attr alone over the bucket each try read, where radosgw
  merges the attrs its request loaded, and sets the lc entry only once the
  attr is stored; the delete removes the entry whether or not the bucket
  held a configuration, as radosgw does.

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
  rgw-go decrypts SSE-C in phase 4, as "Key management" above requires;
  radosgw decrypts it. GetObjectAttributes, which makes GetObject's checks,
  answers the same 501, where radosgw makes them without setting up a
  decryption, because it reads no data, and answers the attributes
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
  rgw-go makes each compressed block from at most its own
  `rgw_max_chunk_size` of plaintext, as radosgw does, so its objects expose
  a coexisting radosgw to the loop only where that radosgw's
  `rgw_max_chunk_size` is the smaller, as a radosgw configured with the
  larger value would.
- **GetObjectTagging and GetBucketTagging of a tag set that does not
  decode.** rgw-go answers 500 UnknownError when an object's or a bucket's
  `user.rgw.x-amz-tagging` decodes neither as a tag set nor as the
  URL-encoded text older objects store. radosgw, by code reading, answers
  200 with no body (`rgw_rest_s3.cc:746-773` and `:839-866` at v19.2.6,
  `:829-856` and `:921-948` at v20.2.4; `docs/ceph-upstream-bugs.md`, "[radosgw
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
  refuse the request, and serves a GET's body. rgw-cache, radosgw's nginx
  cache front end, adds the header unsigned to the authentication
  subrequest it sends with a client's GET
  ([nginx-default.conf:13-56](https://github.com/ceph/ceph/blob/v19.2.7/examples/rgw/rgw-cache/nginx-default.conf#L13-L56));
  to rgw-go's S3 API it is an ordinary header, never read or stored, so
  it may go unsigned on any method ("The S3 API reads X-Amz- headers
  only"), where radosgw stores it on a write as `user.rgw.x-amz-auth`.
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
- **Stored checksums are not shown.** A Tentacle radosgw answers a GET or
  HEAD carrying `x-amz-checksum-mode: enabled`, without a Range, with the
  checksum stored in `user.rgw.cksum` as an `x-amz-checksum-<algorithm>`
  header and `x-amz-checksum-type` (`rgw_rest_s3.cc:314-318` and
  `:548-586` at v20.2.4), and fills GetObjectAttributes' `Checksum`, and
  each listed part's checksum, from the stored ones (`:4029-4057`,
  `:4091-4094`). rgw-go's op layer picks the checksum each of those reads
  reports, and ListParts' `ChecksumAlgorithm`, `ChecksumType` and part
  checksums, by the rules the entries from "A partNumber read shows its
  part's checksum bare" to "An empty Range leaves checksum mode on", below,
  describe. Its S3 handler does not render them yet: rgw-go sends neither
  header, an empty `<Checksum></Checksum>` and no checksum element in
  ListParts, so it hides a checksum a Tentacle radosgw shows. Squid's
  radosgw shows none.
- **No x-amz-expiration.** radosgw computes `x-amz-expiration` for a GET
  or HEAD from the bucket's lifecycle rules (`get_s3_expiration_header`,
  `rgw_rest_s3.cc:112-118`, `:389` and `:462` at v19.2.6; `:117-123`,
  `:400` and `:498` at v20.2.4). rgw-go reads no lifecycle configuration
  until phase 2, so it sends the header for no object; for a bucket
  without lifecycle rules the two agree.
- **No object-lock headers.** On a bucket with object lock enabled,
  radosgw sends `x-amz-object-lock-mode` and
  `x-amz-object-lock-retain-until-date` from an object's retention, and
  `x-amz-object-lock-legal-hold` from its legal hold, to a requester
  allowed `s3:GetObjectRetention` or `s3:GetObjectLegalHold`
  (`rgw_op.cc:1007-1010` and `rgw_rest_s3.cc:595-613` at v19.2.6;
  `rgw_op.cc:1148-1164` and `rgw_rest_s3.cc:712-730` at v20.2.4). rgw-go
  sends none of them until phase 2 serves object lock. Tentacle also
  refuses a system request on such a bucket that lacks either permission
  (`rgw_op.cc:1150-1163` at v20.2.4); rgw-go makes neither check.
- **A restore attr that does not decode.** On Tentacle, rgw-go omits
  `x-amz-restore`, and the cloud tier's storage class a temporary restored
  copy reports, when `user.rgw.restore-status` or `user.rgw.restore-type`
  is empty or `user.rgw.restore-expiry-date` is shorter than its eight
  bytes, and logs a warning. radosgw ends its process on a GET or HEAD of
  such an object (`docs/ceph-upstream-bugs.md`, "[Tentacle's radosgw
  terminates on a GET or HEAD of an object whose restore attr does not
  decode](ceph-upstream-bugs.md#tentacles-radosgw-terminates-on-a-get-or-head-of-an-object-whose-restore-attr-does-not-decode)").
- **HEAD ?acl.** rgw-go answers a HEAD of an object's or a bucket's `?acl`
  with the headers of the GET, whose Content-Length counts the XML
  declaration, and no body. radosgw sends the ACL document, without the
  declaration, as a body after the headers, with a Content-Length of that
  document
  (`docs/ceph-upstream-bugs.md`, "[radosgw sends the ACL document as a body
  after the headers of a HEAD
  ?acl](ceph-upstream-bugs.md#radosgw-sends-the-acl-document-as-a-body-after-the-headers-of-a-head-acl)").
- **A mapped attr never frames the response.** When
  `rgw_extended_http_attrs` names `content-length`, `transfer-encoding`,
  `connection`, `trailer`, `keep-alive`, `upgrade`, `te` or
  `proxy-connection`, rgw-go drops the header the matching object attr,
  such as `user.rgw.content_length` or `user.rgw.trailer`, would give a GET
  or HEAD, and frames the response itself. net/http reads four of these
  names from a handler's headers, Content-Length, Transfer-Encoding,
  Connection and Trailer, a Trailer declaring trailers that would never
  come (`net/http/server.go:1360-1408` at Go 1.27.1); it never reads
  Keep-Alive, Upgrade, TE or Proxy-Connection, which rgw-go drops only as a
  precaution, since they are hop-by-hop headers. radosgw sends such
  an attr's value as one more header beside the
  framing it writes (`rgw_rest_s3.cc:544-571` and `:617-621` at v19.2.6,
  `:661-688` and `:734-738` at v20.2.4). No PUT through radosgw stores
  `user.rgw.content_length`: beast hands a request's Content-Length to
  radosgw as `CONTENT_LENGTH`, not the `HTTP_CONTENT_LENGTH` the option's
  mapping reads (`rgw_asio_client.cc:41-44` and `rgw_rest.cc:199-208` at
  v19.2.6 and v20.2.4), so that attr exists only where something else
  wrote it. A request's other such headers, Transfer-Encoding and
  Connection among them, do reach the mapping, as `HTTP_TRANSFER_ENCODING`,
  `HTTP_CONNECTION` and the like (`rgw_asio_client.cc:50-65`;
  `rgw_rest.cc:2258-2265` at v19.2.6, `:2280-2287` at v20.2.4), so under
  the option a PUT radosgw serves can record them and a later GET sends
  them back. Every other name a mapped attr shares with a header radosgw
  sends, such as Content-Type or ETag, is sent twice by both gateways, in
  radosgw's order, except Date: radosgw sends a mapped Date and then the
  current one (`ClientIO::complete_header`, `rgw_asio_client.cc:143-165`
  at both tags), while net/http adds its own Date only when the handler
  set none (`net/http/server.go:1494`), so rgw-go sends the stored Date
  alone.
- **A stored header value's CR and LF go out as spaces.** radosgw writes a
  header value from an object's attrs as its bytes, dropping only a
  trailing NUL (`rgw_sanitized_hdrval`, `rgw_rest.h:29-47`, and
  `ClientIO::send_header`, `rgw_asio_client.cc:167-181`, at v19.2.6 and
  v20.2.4). net/http rewrites each CR and LF in a header value to a space
  and trims the value's ends (`net/http/header.go:139` and `:206-207` at
  Go 1.27.1). So where a stored value holds CR or LF, rgw-go sends one
  header with spaces in their place, and radosgw sends the bytes, which
  start new header lines or end the header section. A radosgw POST upload
  stores such values from its `x-amz-meta-*` fields
  (`docs/ceph-upstream-bugs.md`, "[radosgw stores a POST upload's
  x-amz-meta fields with their CR and LF and sends them raw on
  GET](ceph-upstream-bugs.md#radosgw-stores-a-post-uploads-x-amz-meta-fields-with-their-cr-and-lf-and-sends-them-raw-on-get)")
  and its `x-amz-website-redirect-location` field, which the website
  endpoint sends as its Location ("[radosgw stores a POST upload's
  x-amz-website-redirect-location with its CR and LF and sends it
  raw](ceph-upstream-bugs.md#radosgw-stores-a-post-uploads-x-amz-website-redirect-location-with-its-cr-and-lf-and-sends-it-raw)");
  rgw-go's website endpoint sends that Location through net/http the same
  way.
- **A user metadata name that is not a valid field name is not sent.**
  A PUT takes `x-amz-meta-*` names from the query string as well as the
  headers, and radosgw url-decodes a query name before it reads it
  (`map_qs_metadata`, `rgw_rest_s3.cc:2581-2595`, and `RGWHTTPArgs::parse`,
  `rgw_common.cc:865`, at v19.2.6; `rgw_rest_s3.cc:2742-2756` and
  `rgw_common.cc:878` at v20.2.4), so a stored name can hold bytes a header
  name may not. net/http drops a header whose name is not a valid field
  name (`net/http/header.go:198-203` at Go 1.27.1), so a GET or HEAD from
  rgw-go omits that metadata header, where radosgw sends the name as stored
  (`ClientIO::send_header`, `rgw_asio_client.cc:167-181` at v19.2.6 and
  v20.2.4).
- **Tentacle's x-amz-delete-marker is false, or absent, on a missing
  object.** On Tentacle, radosgw's `read_permissions` adds
  `x-amz-delete-marker` to a GET or HEAD whose object policies were
  refused with `-ENOENT`, after it loads the key's state following the
  olh: `true` when the key's current version is a delete marker, otherwise
  `false` (`rgw_rest.cc:1936-1945` at v20.2.4; `follow_olh`,
  `driver/rados/rgw_rados.cc:9760-9764`). That refusal is
  `read_obj_policy`'s for a missing object, sent to an admin or to a
  requester who may list the bucket (`rgw_op.cc:453-477` at v20.2.4), and
  to ListParts of a missing upload, whose policies are its meta object's
  while the state loaded is the key's (`:441-448`). v19.2.6 adds no such
  header (`rgw_rest.cc:1893-1933`). In the object reads rgw-go serves, and
  in ListParts of a missing upload, it sends the header where Tentacle
  does, always `false`, also for a key whose current version is a delete
  marker, where Tentacle sends `true`: the object store reports the marker
  (`ObjectState.DeleteMarker`), which the S3 handler does not read. The
  object GETs rgw-go does not serve yet, GetObjectRetention,
  GetObjectLegalHold and `?layout`, answer 501 NotImplemented without
  the header for a missing object as for any other, where Tentacle
  answers 404 with `x-amz-delete-marker: false` to a requester who may
  list the bucket (`RGWHandler_REST_Obj_S3::op_get`,
  `rgw_rest_s3.cc:5360-5378` at v20.2.4). A website GET of a missing key
  whose bucket's error document is missing too gets the header twice from
  Tentacle, once from each key's `read_permissions`, the error document's
  in `serve_errordoc`
  ([rgw_rest_s3.cc:6011](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6011)), and once from rgw-go.
- **A website redirect's Location carries the new key percent-encoded.**
  radosgw builds a website redirect's Location as the protocol, `://`, the
  host, `/` and the new key: `ReplaceKeyPrefixWith` followed by the
  request's key less the prefix, or `ReplaceKeyWith`, or the key, written
  raw (`RGWBWRoutingRule::apply_rule`,
  [rgw_website.cc:40-63](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L40-L63),
  [rgw_website.cc:40-63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L40-L63)). The request's key arrives
  URL-decoded, so its CR and LF reach the header, and its `?`, `#` and `%`
  turn part of it into a query or a fragment or make the Location an
  invalid URI (`docs/ceph-upstream-bugs.md`, "[radosgw writes a website
  redirect's key, host and protocol raw into its
  Location](ceph-upstream-bugs.md#radosgw-writes-a-website-redirects-key-host-and-protocol-raw-into-its-location)").
  rgw-go builds the same new key and percent-encodes it as radosgw's
  `url_encode` encodes a key with the slash kept: every byte at or below
  0x20 or at or above 0x7F, and each of
  ``" # % & + , : ; < = > ? @ [ \ ] ^ ` { }``, becomes `%XX` in upper-case hex
  ([rgw_common.cc:1736-1793](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1736-L1793),
  [rgw_common.cc:1799-1856](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1799-L1856)), so the Location names the
  object the rule names (`website.Apply`, `internal/website/redirect.go`).
  A key of letters, digits, `/` and `! $ ' ( ) * - . _ | ~` gives the
  Location radosgw gives.
- **A website redirect is refused for a host holding a control byte or a
  protocol other than http or https.** radosgw writes a redirect's
  protocol, `://` and host into the Location raw, the rule's `Protocol`
  and `HostName`, else the request's (`RGWBWRoutingRule::apply_rule`,
  [rgw_website.cc:45-59](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L45-L59),
  [rgw_website.cc:45-59](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L45-L59)), and its PutBucketWebsite
  stores any `Protocol` and `HostName`, so a `HostName` written with
  `&#13;&#10;` puts a CR and an LF into the header
  (`docs/ceph-upstream-bugs.md`, "[radosgw writes a website redirect's
  key, host and protocol raw into its
  Location](ceph-upstream-bugs.md#radosgw-writes-a-website-redirects-key-host-and-protocol-raw-into-its-location)").
  rgw-go refuses at both ends. PutBucketWebsite answers 400
  InvalidArgument, with no message, for a `HostName`, in
  `RedirectAllRequestsTo` or a rule's `Redirect`, that holds a byte at or
  below 0x20, or 0x7F, where radosgw stores it (`website.ParseXML`); a
  `Protocol` other than `http` or `https` is already MalformedXML there
  ("PutBucketWebsite answers MalformedXML for a document that does not
  parse or fit S3's schema", above). A redirect whose host, the rule's or
  the request's, holds such a byte, or whose protocol is not `http` or
  `https`, is declined (`website.Apply`, `ErrUnsafeRedirect`), and the
  website endpoint answers 500 InternalError where radosgw sends the
  redirect, with its HTML page, which neither a routing rule nor
  the error document then replaces (`websiteGet.errorHandler`,
  `internal/s3/website_serve.go`). That covers a configuration radosgw stored, which
  rgw-go reads as it is. A host of other bytes, non-ASCII ones included,
  is written as it is, as radosgw writes it.
- **A website redirect to the request's host is refused for a request with
  no Host.** radosgw builds a routing rule's Location with the request's
  `HTTP_HOST` when the rule names no host, an empty one when the request
  sent none or sent it empty
  ([rgw_rest_s3.cc:5424](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5424) and
  [rgw_rest_s3.cc:5555](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5555),
  [rgw_rest_s3.cc:5950](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5950) and
  [rgw_rest_s3.cc:6081](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6081)), so the Location starts
  `http:///` or `https:///` and goes on with the key. RFC 9110 section
  4.2.1 forbids a sender to generate an `http` URI with an empty host, and
  a URL parser that follows the WHATWG standard takes the key's first
  segment for the host (`docs/ceph-upstream-bugs.md`, "[radosgw writes a
  website redirect's key, host and protocol raw into its
  Location](ceph-upstream-bugs.md#radosgw-writes-a-website-redirects-key-host-and-protocol-raw-into-its-location)").
  rgw-go refuses such a redirect (`op.WebsiteRedirect`,
  `internal/op/website_get.go`) with 400 InvalidRequest, since the omission
  is the client's: the row radosgw's error table gives `ERR_INVALID_REQUEST`
  and `ERR_ZERO_IN_URL`
  ([rgw_common.cc:62](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L62) and
  [rgw_common.cc:136](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L136),
  [rgw_common.cc:63](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L63) and
  [rgw_common.cc:137](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L137)), the `ERR_ZERO_IN_URL` that
  `RGWREST::preprocess` answers a request path holding a NUL with
  ([rgw_rest.cc:2189-2190](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2189-L2190),
  [rgw_rest.cc:2206-2207](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2206-L2207)). The refusal matches
  `website.ErrUnsafeRedirect` but keeps its 400, where the stored host and
  protocol refusals above are 500, and like them it meets no routing rule
  and no error document. A rule that names its host redirects whatever
  Host the request sent. net/http refuses an HTTP/1.1 request without a Host header
  ("Host header refusals", above), so the case is an HTTP/1.0 request
  without one, or a request whose Host header is empty.
- **A website request's file test reads its key decoded once.** Whether a
  website request's key names an object decides between reading the key
  and reading the key, `/` and the index suffix (`get_effective_key`,
  [rgw_website.cc:116-133](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L116-L133),
  [rgw_website.cc:116-133](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L116-L133)). radosgw stats the key after
  `url_decode` has decoded it a second time (`web_dir`,
  [rgw_rest_s3.cc:5346-5370](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5346-L5370),
  [rgw_rest_s3.cc:5876-5896](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5876-L5896)), so a key holding `%` is
  stat'ed under another name: `100%` as `100`, and a key with a `%` before
  a byte that is no hex digit as the empty key, which names no object
  (`docs/ceph-upstream-bugs.md`, "[radosgw's website file test decodes the
  key a second
  time](ceph-upstream-bugs.md#radosgws-website-file-test-decodes-the-key-a-second-time)").
  rgw-go stats the key the request names (`GetObject.isFile`,
  `internal/op/website_get.go`), so a website GET of an object `100%`
  serves it where radosgw reads `100%/index.html`. A key holding no `%`,
  and no `+` after a `?`, is stat'ed alike.
- **An object's website redirect is answered once.** radosgw answers a
  website GET or HEAD of an object holding
  `x-amz-website-redirect-location` with 301, that Location and no body,
  from the op's own response
  (`RGWGetObj_ObjStore_S3Website::send_response_data`,
  [rgw_rest_s3.cc:274-292](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L274-L292),
  [rgw_rest_s3.cc:271-289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L271-L289)), and on a GET with data to
  read it sends that response twice (`docs/ceph-upstream-bugs.md`,
  "[radosgw answers a website GET of an object with a redirect location
  twice](ceph-upstream-bugs.md#radosgw-answers-a-website-get-of-an-object-with-a-redirect-location-twice)").
  rgw-go answers it once, decided once the read has the attrs and before
  anything is sent (`GetObject.websiteObjectRedirect`,
  `internal/op/website_get.go`), where radosgw answers it: on success, and
  in place of each refusal the read makes once it has the attrs, which is
  past the part redirect, the conditionals among them; a part refusal
  stands ("A website GET of a part the object has not is InvalidPart under
  any policy", below). A bucket's error document holding the attr is one
  more case: radosgw's `serve_errordoc` reads it with the website
  GetObject, with its data for a GET and a HEAD alike
  ([rgw_rest_s3.cc:5450-5464](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5450-L5464),
  [rgw_rest_s3.cc:5976-5990](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5976-L5990)), and answers a document with
  data with that 301 twice; rgw-go answers the document's redirect once,
  for a GET and a HEAD, in place of the error (`websiteGet.serveErrorDoc`,
  `internal/s3/website_serve.go`). The Location is the attr up to its
  first NUL, as radosgw reads it. An empty attr, which radosgw's POST upload never
  writes and which radosgw copies from a null pointer
  ("[radosgw copies an empty x-amz-website-redirect-location attr from a
  null
  pointer](ceph-upstream-bugs.md#radosgw-copies-an-empty-x-amz-website-redirect-location-attr-from-a-null-pointer)"),
  is an empty Location, as an attr of a NUL alone is on radosgw.
- **A website GET of a part the object has not is InvalidPart under any
  policy.** radosgw's website GetObject reads the redirect attr from the
  attrs `Read::prepare` copies past its part redirect, whose refusals come
  before
  ([rgw_rados.cc:7066-7125](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7066-L7125),
  [rgw_rados.cc:7728-7787](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7728-L7787)), so a GET or HEAD with a
  `partNumber` an object holding `x-amz-website-redirect-location` has not
  is 400 InvalidPart. When a bucket, identity or session policy names an
  object tag or resource tag condition, its permission check loads the
  object's attrs first, and the same request is the object's 301
  (`docs/ceph-upstream-bugs.md`, "[radosgw's website GetObject redirects a
  missing part only under a tag-conditioned
  policy](ceph-upstream-bugs.md#radosgws-website-getobject-redirects-a-missing-part-only-under-a-tag-conditioned-policy)").
  rgw-go answers InvalidPart whatever the policies name
  (`GetObject.websiteObjectRedirect`, `internal/op/website_get.go`).
- **A website redirect's code below 200 or above 999 is answered 301.**
  radosgw answers a website redirect, retarget's or the error handler's,
  at the `HttpRedirectCode` its rule stores, and `abort_early` answers one
  of 200 with WebsiteRedirect's 301
  ([rgw_rest.cc:702-706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L702-L706),
  [rgw_rest.cc:707-711](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L707-L711)). Only the admin API's metadata JSON
  stores a code outside 301-399 (`RGWRedirectInfo::decode_json`,
  [rgw_website.cc:142-148](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_website.cc#L142-L148),
  [rgw_website.cc:142-148](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_website.cc#L142-L148)), and radosgw writes any other code
  into its status line as it is (`ClientIO::send_status`,
  [rgw_asio_client.cc:109-118](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L109-L118),
  [rgw_asio_client.cc:109-118](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_client.cc#L109-L118)), a code its status names
  lack with a null reason phrase (`docs/ceph-upstream-bugs.md`,
  "[radosgw writes a status its name table lacks with a null reason
  phrase](ceph-upstream-bugs.md#radosgw-writes-a-status-its-name-table-lacks-with-a-null-reason-phrase)").
  net/http panics at a status outside 100-999, writes 100-199 other than
  101 as an interim response that does not end the response, and 101 as a
  switch of protocol (`net/http/server.go:1166-1180` and `:1223-1227` at
  Go 1.27.1), none of them the redirect radosgw's status line names, so
  rgw-go answers a code below 200 or above 999 with 301
  (`redirectStatus`, `internal/s3/website_serve.go`). A code from 201 to
  999 is answered as stored, from 400 on with the HTML error page, which
  names no Code, as radosgw's does.
- **A website error document is read whole and unconditionally.** radosgw
  serves a bucket's error document in place of an error its website
  endpoint meets before the GetObject executes, under that error's status
  (`serve_errordoc`,
  [rgw_rest_s3.cc:5460-5531](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5460-L5531),
  [rgw_rest_s3.cc:5986-6057](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5986-L6057)). It clears the read's Range and
  conditionals, but the read's execute takes them back from the request,
  with its `partNumber`, `?torrent` and, on Tentacle, its checksum mode
  (`docs/ceph-upstream-bugs.md`, "[radosgw's website error document read
  takes the request's Range and conditionals
  back](ceph-upstream-bugs.md#radosgws-website-error-document-read-takes-the-requests-range-and-conditionals-back)"),
  so a Range serves part of the document under the error's status. An
  If-None-Match or If-Modified-Since the document meets answers the
  error's status with the document's Last-Modified, ETag, Cache-Control
  and Expires, and no body or Content-Type
  ([rgw_rest_s3.cc:631-646](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L631-L646),
  [rgw_rest_s3.cc:740-755](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L740-L755)); a conditional the document
  fails, a part it has not, or any other failure of the read's execute,
  an SSE-C document read without its key among them, answers the error's
  status with an empty `binary/octet-stream` body. rgw-go reads the
  document whole and unconditionally, with the request's SSE-C headers and
  `response-*` overrides, which radosgw reads outside `get_params`, and
  answers the original error's HTML page when that read fails before its
  response starts, as radosgw does for a failure before the read's
  execute, such as a missing or private document
  (`websiteGet.serveErrorDoc`, `internal/s3/website_serve.go`).
- **A website HEAD's error document has no body.** radosgw reads a
  bucket's error document for a website HEAD as for a GET, with its data,
  and sends that data after the HEAD's header, past the response's end
  (`docs/ceph-upstream-bugs.md`, "[radosgw sends a website HEAD the error
  document's
  body](ceph-upstream-bugs.md#radosgw-sends-a-website-head-the-error-documents-body)").
  rgw-go answers the HEAD with the document's headers alone
  (`websiteGet.serveErrorDoc`, `internal/s3/website_serve.go`).
- **A website error document is served for a 4XX error only.** radosgw's
  website error handler serves the bucket's error document for every
  error that reaches it once the bucket has loaded, whatever its status,
  under that status, and under -1, with a null reason phrase, for an
  error its S3 table has no row for (`docs/ceph-upstream-bugs.md`,
  "[radosgw serves a website's custom error document for every error
  status, where S3 serves it for 4XX
  only](ceph-upstream-bugs.md#radosgw-serves-a-websites-custom-error-document-for-every-error-status-where-s3-serves-it-for-4xx-only)"
  and "[radosgw writes a status its name table lacks with a null reason
  phrase](ceph-upstream-bugs.md#radosgw-writes-a-status-its-name-table-lacks-with-a-null-reason-phrase)").
  Amazon S3 "returns your custom error document for only the HTTP 4XX
  class of error codes"
  (https://docs.aws.amazon.com/AmazonS3/latest/userguide/CustomErrorDocSupport.html),
  and rgw-go serves it for 400-499 only, under the error's status; a 5xx,
  UnknownError among them, gets the HTML error page
  (`websiteGet.errorHandler`, `internal/s3/website_serve.go`). Routing
  rules are unchanged: a rule matches any status, -1 for UnknownError, as
  radosgw's does.
- **A website error document's request is logged under the error's
  status.** radosgw logs a request it answered with the error document
  as a success with status 200, which the document's read sets before it
  writes the custom status
  ([rgw_rest_s3.cc:402-404](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L402-L404),
  [rgw_rest_s3.cc:405-407](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L405-L407)), so its usage entry counts a
  successful op under the bucket's name (`docs/ceph-upstream-bugs.md`,
  "[radosgw logs a served error document as a 200
  success](ceph-upstream-bugs.md#radosgw-logs-a-served-error-document-as-a-200-success)").
  rgw-go logs the status it sent, so a 404's entry counts no successful op
  and takes the bucket name `-` (`op.LogUsage`, `internal/op/usagelog.go`).
- **A partNumber read shows its part's checksum bare.** In the op layer,
  which the S3 handler does not render yet ("Stored checksums are not
  shown"), a checksum-mode GET or HEAD with `partNumber` reports the part
  head's own `user.rgw.cksum` as its armor alone, with
  `x-amz-checksum-type` COMPOSITE only when the record's flags say
  COMPOSITE, as main does
  ([rgw_rest_s3.cc:569-608](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_rest_s3.cc#L569-L608),
  [rgw_cksum.h:417-424](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L417-L424))
  and s3-tests' five `test_multipart_use_cksum_helper_*` expect. A v20.2.4
  radosgw types a part's record as the whole object's, by
  `get_checksum_type` with the object's parts count, which answers
  COMPOSITE for every part's record, as none has V2, and sends
  `<armor>-<parts>`
  ([rgw_rest_s3.cc:548-586](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L548-L586);
  `docs/ceph-upstream-bugs.md`, "[radosgw reports a partNumber read's
  checksum as the multipart object's
  composite](ceph-upstream-bugs.md#radosgw-reports-a-partnumber-reads-checksum-as-the-multipart-objects-composite)").
  A part head without a record shows none, never the object's. Part 1 of
  an object without parts, which both gateways serve as the whole object,
  follows the whole object's rules, where main types its record by its
  flags.
- **A composite checksum on an object without parts is not shown.** In the
  op layer, which the S3 handler does not render yet ("Stored checksums are
  not shown"), a checksum-mode GET or HEAD, and GetObjectAttributes'
  `Checksum`, of an object without parts whose record is flagged COMPOSITE
  and COMBINED report none. A Tentacle radosgw reports such a record's
  armor as the object's FULL_OBJECT checksum
  ([rgw_rest_s3.cc:551-578](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L551-L578),
  [rgw_rest_s3.cc:4029-4050](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4029-L4050),
  [rgw_cksum.h:382-399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L382-L399)),
  which its value is not: it combines the parts of the multipart object it
  was copied from. A radosgw copy or transition carries one there
  (`docs/ceph-upstream-bugs.md`, "[radosgw carries a composite checksum to
  a copy without parts, which Tentacle reports as its FULL_OBJECT
  checksum](ceph-upstream-bugs.md#radosgw-carries-a-composite-checksum-to-a-copy-without-parts-which-tentacle-reports-as-its-full_object-checksum)");
  a copy through rgw-go that streams drops it ("A copy that streams its
  data drops a composite checksum").
- **ListParts reports its upload's checksum algorithm and type.** In the op
  layer, which the S3 handler does not render yet ("Stored checksums are
  not shown"), ListParts reports `ChecksumAlgorithm`, the upload's checksum
  type, and `ChecksumType`, FULL_OBJECT for a CRC upload whose flags do not
  say COMPOSITE and COMPOSITE for any other, as its completion combines
  the parts, both from the upload's `multipart_upload_info` and only for a
  type AWS defines, under AWS's names, so SHA512 and XXHASH3 too. A
  Tentacle radosgw takes `ChecksumAlgorithm` from the meta object's
  `user.rgw.cksum`, which nothing writes, so it reports it for no upload,
  and it renders no `ChecksumType`
  ([rgw_op.cc:7617-7630](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7617-L7630),
  [rgw_rest_s3.cc:4657-4719](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4657-L4719);
  `docs/ceph-upstream-bugs.md`, "[radosgw's ListParts never reports its
  upload's checksum
  algorithm](ceph-upstream-bugs.md#radosgws-listparts-never-reports-its-uploads-checksum-algorithm)").
  Each part's checksum is its part record's, only for a type AWS defines,
  as radosgw renders one
  ([rgw_rest_s3.cc:4709-4713](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4709-L4713));
  under AWS's names that takes in a SHA512 or XXHASH3 part, whose type a
  v20.2.4 radosgw does not count as AWS's
  ([rgw_cksum.h:88-99](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99)).
  A Squid upload has no checksum type and its part records hold no
  checksum ("Squid clusters get single-object and part checksums, not an
  upload's"), so ListParts on Squid reports neither, as Squid's radosgw
  does.
- **Squid clusters show the checksums they store.** In the op layer, which
  the S3 handler does not render yet ("Stored checksums are not shown"),
  the read rules of these entries apply on Squid as on Tentacle: a
  checksum-mode GET or HEAD, and GetObjectAttributes, which rgw-go serves
  on Squid too, show the `user.rgw.cksum` rgw-go stores there, a single
  object's or a part head's ("Squid clusters get single-object and part
  checksums, not an upload's"), and GetObjectAttributes lists each part's.
  Squid's radosgw has no checksum code and shows none. No Squid completion
  combines a checksum, so a whole multipart object shows none there.
- **A stored checksum rgw-go cannot read whole is not shown.** rgw-go's
  stores read a `user.rgw.cksum` with bytes past its record as no
  checksum, as they read a record that does not decode
  (`cksum.DecodeAttr`), and its reads show no checksum of type none, which
  holds no digest. Either way a checksum-mode GET or HEAD, and
  GetObjectAttributes' `Checksum` and parts, show none and the read
  succeeds; a retried CompleteMultipartUpload reports no checksum for the
  first. No radosgw writes either form. A Tentacle radosgw's checksum-mode
  GET or HEAD of an object whose attr has bytes past its record ends the
  radosgw process, as its decode of the whole buffer asserts
  ([encoding.h:632-638](https://github.com/ceph/ceph/blob/v20.2.4/src/include/encoding.h#L632-L638);
  `docs/ceph-upstream-bugs.md`, "[radosgw aborts on a checksum-mode GET of
  an object whose checksum attr has bytes past its
  record](ceph-upstream-bugs.md#radosgw-aborts-on-a-checksum-mode-get-of-an-object-whose-checksum-attr-has-bytes-past-its-record)"),
  while GetObjectAttributes, ObjectParts and a retried completion read the
  record and show it
  ([rgw_rest_s3.cc:4031-4036](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4031-L4036),
  [rgw_sal_rados.cc:2912-2917](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2912-L2917),
  [rgw_op.cc:7475-7480](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7475-L7480)).
  For a record of type none it sends an `x-rgw-checksum-none` header and a
  `ChecksumNONE` element, neither holding a digest, where ObjectParts
  skips one
  ([rgw_rest_s3.cc:4091-4094](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4091-L4094)).
  A copy that streams its data drops an attr with bytes past its record,
  as it drops any attr it cannot read, where radosgw carries it ("A copy
  that streams its data drops a composite checksum").
- **An empty Range leaves checksum mode on.** rgw-go takes a `Range` header
  sent with an empty value for no Range ("An empty header reads as no
  header", above), so in the op layer, which the S3 handler does not
  render yet ("Stored checksums are not shown"), a checksum-mode GET or
  HEAD carrying one reports the stored checksum. radosgw takes the empty
  header for a Range, which turns checksum mode off
  (`RGWGetObj_ObjStore::get_params`, for GET and HEAD alike,
  [rgw_rest.cc:843](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L843)
  at v19.2.7 and
  [rgw_rest.cc:843](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L843)
  at v20.2.4;
  [rgw_rest_s3.cc:551](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L551)).
- **A read that replays a pending modification answers the version current
  after it.** A GET, a HEAD and every other read of a versioned key named
  without a version id follow the key's OLH. radosgw's `follow_olh` runs
  `update_olh` while the OLH head carries a pending modification, then
  resolves the current version from the head as it read it before that
  replay, and `get_olh_target_state` reads that version's state
  ([rgw_rados.cc:8984-9018](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8984-L9018),
  [rgw_rados.cc:6238-6258](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6238-L6258);
  [rgw_rados.cc:9730-9766](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9730-L9766),
  [rgw_rados.cc:6852-6872](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6852-L6872)).
  When the modification is the deletion of the current version by its
  version id, whose writer has not run its own replay, the replay removes
  that version's head and radosgw answers 404 NoSuchKey though the
  previous version is current (`docs/ceph-upstream-bugs.md`, "[radosgw's
  follow_olh answers the version that was current before the replay it
  runs](ceph-upstream-bugs.md#radosgws-follow_olh-answers-the-version-that-was-current-before-the-replay-it-runs)").
  rgw-go reads the OLH head again once the replay has run and resolves the
  version from that read, running no second replay (`followHead`,
  `internal/driver/stat.go`): it answers the version current after the
  replay, there the previous one. The replay writes what radosgw's writes;
  the cost is one more head read, only on a read that ran a replay. A
  second read that finds the head gone, the replay having removed the
  key's last version, answers 404, and one that finds the head no longer
  an OLH head answers the object it finds. A conditional versioned write
  evaluates its condition the same way. Each
  attempt follows the key's OLH head, replaying a pending modification,
  reads the head again when the replay applied anything or another writer
  refused a pass of it, and evaluates the condition on the version that
  read finds current (`followOLHHead`, `internal/driver/olh_cond.go`).
  radosgw's precondition check reads the current version through the same
  `follow_olh`
  ([rgw_rados.cc:3187-3197](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3187-L3197) and
  [rgw_rados.cc:6624-6644](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6624-L6644) at v19.2.7,
  [rgw_rados.cc:3284-3294](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3284-L3294) and
  [rgw_rados.cc:7238-7258](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7258) at v20.2.4),
  so it evaluates on the version current before the replay. When the
  replay is refused, `follow_olh` turns `update_olh`'s ECANCELED into ENOENT
  and the write evaluates on no current version
  ([rgw_rados.cc:8989-8995](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8989-L8995) and
  [rgw_rados.cc:3190-3191](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3190-L3191) at v19.2.7,
  [rgw_rados.cc:9735-9741](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9735-L9741) and
  [rgw_rados.cc:3287-3288](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3287-L3288) at v20.2.4),
  where rgw-go reads the head again and evaluates on that read. So while
  another writer's link stays logged and unreplayed, an If-Match on the
  linked version's ETag answers 412 through radosgw and succeeds through
  rgw-go, and one on the version before it the other way round. Both
  gateways' replays write the same state. Versioned writes are not served
  yet.
- **A read of a versioned key named without a version id sends no
  x-amz-version-id.** radosgw's GET and HEAD answer with the id of the
  version they resolved: `Read::prepare` keeps the version it followed to
  ([rgw_rados.cc:7105](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7105),
  [rgw_rados.cc:7767](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7767)),
  `RadosReadOp::prepare` copies its instance to the request's object
  ([rgw_sal_rados.cc:2864](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2864),
  [rgw_sal_rados.cc:3699](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L3699)),
  and `RGWGetObj` takes `version_id` from that object
  ([rgw_op.cc:2260](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2260),
  [rgw_op.cc:2496](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L2496))
  and sends it
  ([rgw_rest_s3.cc:469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L469),
  [rgw_rest_s3.cc:497](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L497)).
  Tentacle's GetObjectAttributes does the same in
  `RGWGetObjAttrs_ObjStore_S3::send_response`
  ([rgw_rest_s3.cc:3999-4010](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3999-L4010)).
  rgw-go's GET, HEAD and GetObjectAttributes send the version id the
  request named, so a read of a versioned key named without one sends no
  `x-amz-version-id` where radosgw sends the current version's. Neither
  sends one when the current version is the null version, whose instance
  is empty. Task V14a's GET and HEAD send the resolved version's id
  (decision D4), which retires this difference.

### Object write differences

rgw-go writes an object's tails, head and bucket index entry as radosgw's
`AtomicObjectProcessor` and `RGWRados::Object::Write::write_meta` do at the
v19.2.6 and v20.2.4 tags, and queues an overwritten object's tails for the
GC as `complete_atomic_modification` does. Where the two differ, rgw-go
does the following.

- **A body cut short is 400 RequestTimeout.** When a client closes its
  connection before it has sent the body its Content-Length promised,
  rgw-go answers 400 RequestTimeout, the answer radosgw's short-body branch
  intends for such a body (`!chunked_upload && ofs != s->content_length`,
  `rgw_op.cc:4430-4431` at v19.2.6, `:4662-4663` at v20.2.4; `bodyErr`,
  `internal/driver/stripe.go`). A chunked body cut short, which that branch
  exempts, gets the same 400 from rgw-go, as net/http reports either cut as
  `io.ErrUnexpectedEOF` (`net/http/internal/chunked.go:113-161` at
  go1.27.1). radosgw answers both 404 NoSuchKey: beast reports either cut
  as its own `partial_message` code, 2, which radosgw takes for an errno,
  ENOENT
  (`docs/ceph-upstream-bugs.md`, "[radosgw's beast frontend passes Boost
  error codes off as errno](ceph-upstream-bugs.md#radosgws-beast-frontend-passes-boost-error-codes-off-as-errno)").

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
  If-Match and If-None-Match as `check_preconditions` does at v19.2.7 and
  v20.2.4
  ([rgw_rados.cc:6672-6714](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6672-L6714),
  [rgw_rados.cc:7286-7328](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7286-L7328)),
  on both releases, keeping its comparison of a condition's start over the
  ETag's length. A client of a Squid zone sees one difference from
  v19.2.7: CompleteMultipartUpload honors both headers as v20.2.4 does,
  which takes both
  ([rgw_rest_s3.cc:4565](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4565))
  and hands them to the head's write
  ([rgw_sal_rados.cc:4481-4482](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4481-L4482)), where
  v19.2.7 still reads neither for it
  ([rgw_rest_s3.cc:4094-4104](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4094-L4104))
  and completes over whatever the key holds. A refused completion leaves
  the upload whole, so it can be completed again.

  Squid before v19.2.7 skipped the check on a head with a fake tag
  (`need_guard`), which let a conditional write overwrite an update it was
  meant to protect; [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348)
  fixed it for Tentacle, and its Squid backport,
  [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932), is first
  tagged in v19.2.7 (`docs/ceph-upstream-bugs.md`, "[Squid's write
  conditions fail an If-None-Match ETag on a missing key and skip a head
  without a write
  tag](ceph-upstream-bugs.md#squids-write-conditions-fail-an-if-none-match-etag-on-a-missing-key-and-skip-a-head-without-a-write-tag)").
- **Delete conditions are Tentacle's, and a delete is guarded, on both
  releases.** rgw-go checks a DeleteObject's If-Match,
  `x-amz-if-match-size` and `x-amz-if-match-last-modified-time`, and each
  DeleteObjects `<Object>`'s ETag, Size and LastModifiedTime, as v19.2.7
  and v20.2.4 do, on both releases. Both read the three headers
  ([rgw_rest_s3.cc:3435-3483](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3483),
  [rgw_rest_s3.cc:3685-3733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733))
  and parse the three elements with the same parser
  ([rgw_multi_del.cc:17-62](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_multi_del.cc#L17-L62),
  [rgw_multi_del.cc:17-62](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_multi_del.cc#L17-L62)),
  hand each key's conditions to its delete
  ([rgw_op.cc:6902-6904](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6902-L6904),
  [rgw_op.cc:7830-7832](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7830-L7832)),
  and `Delete::delete_obj` compares them with `check_preconditions` and
  puts an `obj_check_mtime` EQ in the removal op
  ([rgw_rados.cc:6049-6057](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6049-L6057),
  [rgw_rados.cc:6663-6671](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6663-L6671)). Conditional
  delete came upstream with
  [ceph/ceph#63348](https://github.com/ceph/ceph/pull/63348) (tracker
  [#68183](https://tracker.ceph.com/issues/68183)), in v20.2.4 through
  [ceph/ceph#65949](https://github.com/ceph/ceph/pull/65949), and on Squid
  through [ceph/ceph#65932](https://github.com/ceph/ceph/pull/65932),
  first tagged in v19.2.7. The same change dropped the guard v19.2.6's
  delete sends ahead of the head's removal, a `cmpxattr` on the write tag
  the delete read; rgw-go keeps it on both releases. A client of a zone of
  either release sees one difference: a delete whose object another writer
  replaced between the delete's read and its removal fails the removal and
  answers 204 with the replacement standing. v19.2.7 and v20.2.4 remove
  the replacement, even under an If-Match naming the replaced object's
  ETag, and leave the replacement's tails unreferenced
  (`docs/ceph-upstream-bugs.md`, "[radosgw's DeleteObject removes the head
  without checking its write
  tag](ceph-upstream-bugs.md#radosgws-deleteobject-removes-the-head-without-checking-its-write-tag)").
  The removal op carries the `cmpxattr` step neither sends.
- **DeleteObjectTagging keeps the object's mtime on Squid too.** rgw-go
  removes an object's tags as v20.2.4's `RGWDeleteObjTags::execute` does,
  with the object's attrs loaded first, so the head and its index entry
  keep the stored mtime advanced by the nanosecond every attr change adds
  (`rgw_op.cc:1351-1375` at v20.2.4). v19.2.6 removes them without loading
  the object, and stamps it with the epoch plus a nanosecond
  (`rgw_op.cc:1133-1139` at v19.2.6; `docs/ceph-upstream-bugs.md`, "[Squid's
  DeleteObjectTagging stamps the object with the epoch plus one
  nanosecond](ceph-upstream-bugs.md#squids-deleteobjecttagging-stamps-the-object-with-the-epoch-plus-one-nanosecond)").
  A client of a Squid zone sees the object's own Last-Modified after the
  tags are removed, where v19.2.6 reports 1970-01-01.
- **Writes to a versioned or object-lock bucket answer 501 until
  versioning is served.** Every write rgw-go serves but the deletes,
  PutObject, CopyObject, PutObjectAcl, PutObjectTagging,
  DeleteObjectTagging and CompleteMultipartUpload, takes radosgw's
  unversioned path, where radosgw's CompleteMultipartUpload writes a new
  version, or the null one through the OLH (`rgw_op.cc:6459-6466` at
  v19.2.6, `:7284-7291` at v20.2.4). On a bucket whose
  versioning is enabled or suspended, or that has object lock, that path
  would write a version's head beside the bucket's OLH under an index
  entry for the plain key. DeleteObject and DeleteObjects would reach a
  store whose delete takes radosgw's versioned path on a bucket whose flags
  hold `BUCKET_VERSIONED`, but the ops do not yet answer the version id or
  the delete marker it reports, and they would skip the retention and
  legal-hold checks radosgw makes for a versioned delete
  (`verify_object_lock`, `rgw_op.cc:5185-5221` and `:6845-6869` at v19.2.6,
  `:5576-5610` and `:7782-7806` at v20.2.4). rgw-go answers such a write,
  and any write naming a version, a copy's source included, CopyObject's
  and UploadPartCopy's, with 501 NotImplemented before it touches the
  store; DeleteObjects answers it through its status
  line before its first key. radosgw serves them. The bucket listing makes
  the same refusal for a versioned bucket ("The versions of a bucket whose
  versioning was ever enabled are not listed").

  The exception is `versionId=null` on a bucket whose versioning was never
  enabled, which rgw-go takes to be one whose flags hold none of
  `BUCKET_VERSIONED`, `BUCKET_VERSIONS_SUSPENDED` and
  `BUCKET_OBJ_LOCK_ENABLED`. radosgw tests `BUCKET_VERSIONED` alone, and
  never writes either of the others without it: suspending versioning keeps
  it set (`rgw_op.cc:2837-2843` at v19.2.6, `:3069-3075` at v20.2.4), and
  object lock is enabled only with versioning (`driver/rados/rgw_rados.cc:2392`
  and `rgw_op.cc:8232` at v19.2.6, `:2497` and `:9180` at v20.2.4). On such a
  bucket radosgw takes the null version for the plain object: its head is
  the plain key's (`rgw_obj_key::get_oid`, `rgw_obj_types.h:226-254` at both
  releases), and the delete and the attr writes clear the instance before
  they reach the index (`driver/rados/rgw_rados.cc:5768-5770` and
  `:6601-6603` at v19.2.6, `:6456-6458` and `:7401-7403` at v20.2.4), the
  delete taking the unversioned path (`:5774`, `:6462`), as it does for
  each key of a DeleteObjects (`rgw_op.cc:6889` at v19.2.6, `:7826` at
  v20.2.4). rgw-go serves a DeleteObject, a DeleteObjects key,
  PutObjectAcl, PutObjectTagging and DeleteObjectTagging naming it as the
  same request naming no version, but authorizes each for the version's
  action, `s3:DeleteObjectVersion` and the rest, as radosgw authorizes any
  key naming a version (`rgw_op.cc:5145-5147` and `:6829-6831` at v19.2.6,
  `:5535-5537` and `:7766-7768` at v20.2.4); the reads naming it were served
  already. A PutObject, a CompleteMultipartUpload or a CopyObject naming
  the null version, a copy as its destination or its source, still
  answers 501: radosgw writes such a head at the plain key's oid but files
  it in the index under the instance `null`, apart from the plain key's
  entry (`driver/rados/rgw_rados.cc:9473` and `:7133` at v19.2.6,
  `:10405` and `:7984` at v20.2.4; `encode_obj_index_key`,
  `cls/rgw/cls_rgw.cc:339-346` at v19.2.6, `:389-396` at v20.2.4), then
  links the key to an OLH as a versioned object
  ([rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375)
  at v19.2.7; `docs/ceph-upstream-bugs.md`, "[radosgw indexes a
  PutObject naming versionId=null on an unversioned bucket under the null
  instance](ceph-upstream-bugs.md#radosgw-indexes-a-putobject-naming-versionidnull-on-an-unversioned-bucket-under-the-null-instance)"),
  and copies from a source naming it without the checks of a copy onto
  itself (`rgw_rest_s3.cc:3541-3548` at v19.2.6, `:3821-3828` at v20.2.4).
  An UploadPartCopy whose source names it answers 501 too, as one whose
  source names any version does: the exception does not reach that refusal.
- **CompleteMultipartUpload sends no x-amz-version-id.** radosgw's
  completion sends `x-amz-version-id` whenever its `version_id` is not
  empty, on an error as on a success (`dump_header_if_nonempty`,
  `rgw_rest_s3.cc:4081` at v19.2.6, `:4609` at v20.2.4). For an ordinary
  request that is the instance it generates on a bucket whose versioning
  is enabled and not suspended (`rgw_op.cc:6459-6466` at v19.2.6,
  `:7284-7291` at v20.2.4; `versioning_enabled`, `rgw_common.h:1076` at
  v19.2.6, `:1118` at v20.2.4), a completion rgw-go answers with 501
  ("Writes to a versioned or object-lock bucket answer 501 until
  versioning is served"); on an unversioned or suspended bucket radosgw
  sends none either. The other source is a system request's
  `rgwx-version-id`, which `get_system_versioning_params` reads into
  `version_id` whatever the bucket's versioning (`rgw_op.cc:6378` and
  `rgw_op.h:2113-2139` at v19.2.6, `:7174` and `:2280-2306` at v20.2.4),
  so radosgw echoes it from an unversioned bucket's completion. rgw-go
  reads neither it nor `rgwx-versioned-epoch` on any write, and serves
  such a request as one without them, where radosgw's PutObject,
  DeleteObject and CopyObject read both as well (`rgw_op.cc:4177`,
  `:5273` and `:5387` at v19.2.6, `:4386`, `:5662` and `:5953` at
  v20.2.4): PutObject echoes the version id as the completion does
  (`rgw_rest_s3.cc:2748` at v19.2.6, `:2912` at v20.2.4), and CopyObject
  gives its destination the instance named whatever the bucket's
  versioning (`rgw_op.cc:5573-5574` at v19.2.6, `:6139-6140` at
  v20.2.4). A system request whose `rgwx-versioned-epoch` does not
  parse as a number fails all four with -EINVAL, 400 InvalidArgument
  (`rgw_op.h:2121-2130` at v19.2.6, `:2288-2297` at v20.2.4;
  `rgw_common.cc:61` at v19.2.6, `:62` at v20.2.4), where rgw-go
  serves it. No radosgw component sends either parameter at either
  release; they belong to multisite, which is excluded.
- **Writes that ask for encryption answer 501, SSE-S3 and SSE-KMS until
  phase 2 and SSE-C until phase 4.** radosgw
  encrypts a PutObject or CopyObject that sends the SSE-C headers, and
  fails one that asks for SSE-S3 or SSE-KMS, or that lands in a bucket
  with a default encryption, when it has no key server
  (`get_encryption_defaults`, `rgw_rest_s3.cc:146-264` at v19.2.6,
  `:151-269` at v20.2.4). rgw-go encrypts nothing on the write path in phase
  1, so it answers 501 NotImplemented, rather than store in the clear what
  a client asked to have encrypted ("Key management: SSE-KMS and SSE-S3 with
  every backend"). Phase 2 answers a request for SSE-S3 or SSE-KMS, and a
  write into a bucket with a default encryption, with radosgw's failure
  without a key server (item 2 there); an SSE-C write keeps the 501 until
  phase 4 encrypts it. The 501 comes:
  - before the permission check and before reading the body, to a PutObject
    or CopyObject carrying any `X-Amz-` header that radosgw takes for an
    encryption header, one whose name starts `x-amz-server-side-en`
    (`rgw_common.cc:413-464` at v19.2.6, `:426-477` at v20.2.4), and to one
    naming a copy source's SSE-C key; and to a PutObject carrying such a
    query parameter, which `map_qs_metadata` reads (`rgw_rest_s3.cc:2591-2593`
    at v19.2.6, `:2752-2754` at v20.2.4). No such header is stored as an
    attr. radosgw takes the same header under its six other meta prefixes,
    `X-Goog-Server-Side-Encryption-Customer-Key` among them, which rgw-go's
    S3 API does not read, so a write naming encryption under one of them is
    stored unencrypted ("The S3 API reads X-Amz- headers only");
  - once the requester is authorized, to either write into a bucket whose
    attrs hold a default encryption, so a refused requester gets 403 whatever
    the bucket holds;
  - to a CreateMultipartUpload, UploadPart or UploadPartCopy on the same
    terms, since radosgw's `init_meta_info` files their headers the same
    way and its `prepare_encryption` reads them: an UploadPart before the
    permission check, with its query string read too, and an UploadPartCopy
    also for its source's SSE-C key; a CreateMultipartUpload
    once the requester is authorized, as its `get_params` runs in `execute`,
    from its headers alone. A refused upload creates no upload, a refused part
    stores no part, and no such header is stored as an attr.
- **Appends and a PUT that names a copy source but is no copy answer 501.**
  radosgw serves a PUT with `?append` as an append, and a PUT whose
  `x-amz-copy-source` comes with `x-amz-copy-source-range` and no
  `uploadId`, or names no bucket, as a PutObject whose data it reads from
  that source (`RGWPutObj::init_processing` and `execute`, `rgw_op.cc
  :3806-3918` and `:4297-4336` at v19.2.6, `:4015-4127` at v20.2.4). The
  PUT route answers both with 501 NotImplemented. A PUT with a non-empty
  `uploadId` is UploadPart or UploadPartCopy, which the multipart routes
  serve. A PUT whose `uploadId` is present but empty is neither:
  `RGWHandler_REST_S3::init` parses no copy source when `uploadId` is
  present at all (`rgw_rest_s3.cc:5026-5029` at v19.2.6, `:5586-5589` at
  v20.2.4), and `RGWPutObj::execute` takes an empty one for none
  (`rgw_op.cc:4214`, `:4423`), so radosgw serves one naming a copy source
  as a PutObject whose data it reads from that source, which rgw-go answers
  with 501 NotImplemented too; both serve one without a copy source as a
  PutObject.
- **PutObject and CopyObject check their headers before authorizing.**
  radosgw decodes a PutObject's `Content-MD5` in `execute`, after
  `verify_permission` (`rgw_op.cc:4184-4198` at v19.2.6, `:4393-4407` at
  v20.2.4), and builds its attrs, where the three `rgw_max_attr*` limits
  are checked, only once the body is stored (`:4525-4529`, `:4793-4797`);
  a CopyObject builds its attrs in `execute` (`init_common`, `:5523` at
  v19.2.6, `:6089` at v20.2.4). rgw-go makes these checks where radosgw's
  `get_params` runs, once the bucket is loaded and before the permission
  check and the body. So a `Content-MD5` that does not decode to 16 bytes
  and an attr past a limit are answered with their error before a refusal
  of permission, and a PutObject past an attr limit is refused before its
  body is read rather than after it is stored. Both refuse the request;
  only which refusal a refused requester sees differs, and both errors
  depend on the request alone. Neither write builds its ACL before
  authorizing, as the grant headers' grantees are looked up: a malformed
  grant or one naming no user is answered once the requester is
  authorized, as radosgw's CopyObject answers it (`init_dest_policy`,
  `rgw_op.cc:5492` at v19.2.6, `:6058` at v20.2.4), where radosgw's
  PutObject answers it before authorizing, its `create_s3_policy` running
  in `get_params` during `init_processing` (`rgw_rest_s3.cc:2618-2620` at
  v19.2.6, `:2779-2781` at v20.2.4; "PutObject and CopyObject authorize a
  request before they read what the store holds for it"). UploadPart decodes
  `Content-MD5` and builds its attrs once the requester is authorized, as
  radosgw's `execute` does, but before it reads the body: a part past an
  attr limit is refused before its body is read rather than after it is
  stored, and a `Content-MD5` that does not decode is answered ahead of a
  Content-Length over `rgw_max_put_size`, which radosgw's `verify_params`
  answers first.
- **The multipart routes authorize a request before they read what the
  store holds for it.** radosgw reads stored state before its permission
  check. `rgw_build_bucket_policies` refuses a storage class the bucket's
  placement lacks (`rgw_op.cc:576-583` at v19.2.6, `:606-613` at v20.2.4).
  For UploadPart and UploadPartCopy, `RGWPutObj::init_processing` loads
  the copy source's bucket and refuses a public canned ACL under the
  bucket's block of public ACLs (`:3853-3866` and `:3903-3909`;
  `:4062-4075` and `:4112-4118`), its `get_params` looks up the grantees of
  the `x-amz-grant-*` headers and refuses a retention or a legal hold on a
  bucket without object lock (`rgw_rest_s3.cc:2618-2620` and `:2669-2673`;
  `:2779-2781` and `:2830-2834`), and `RGWPutObj::verify_permission` reads
  the copy source, a missing one refused, before it checks the destination
  (`rgw_op.cc:3920-3988`; `:4129-4197`). `read_obj_policy` answers an
  upload's or an object's policy that cannot be read with its error
  (`rgw_op.cc:385-447`; `:415-477`). So a refused requester learns from
  radosgw whether the bucket has such a storage class, a block of public
  ACLs or object lock, whether a named user or account exists, whether a
  copy source's bucket or key exists, and whether a stored policy is
  damaged. rgw-go refuses a request before its permission check only for
  what the request itself shows, its headers, arguments and declared
  length, and makes each of those checks once the requester is
  authorized, so a refused requester gets 403 AccessDenied whatever the
  bucket, the upload, the users or the source hold. The bucket's own
  existence, which authorization needs, is answered first, as radosgw
  answers it. UploadPartCopy reads its source only once the destination
  allows the request and the requester's op mask allows the write, and
  checks the storage class before it reads the source, as radosgw's
  `init_permissions` checks it first. So a requester whose op mask refuses
  the write gets 403 AccessDenied for a missing source bucket, where
  radosgw, which loads the source bucket in `init_processing` before
  `verify_op_mask` (`rgw_process.cc:202` and `:208` at v19.2.6 and
  v20.2.4), answers 404 NoSuchBucket. ListParts authorizes against the
  upload's meta object, as
  radosgw does, and answers a missing upload by the missing-object rule;
  an upload or object whose info cannot be read is authorized as one whose
  policy is the bucket owner's default, and its error is answered only to
  a requester that passes. CreateMultipartUpload makes all of these checks
  where radosgw's `get_params` runs, once the requester is authorized
  (`rgw_op.cc:6299`, `:6968`), but for the storage class, which radosgw
  checks first.
- **PutObject and CopyObject authorize a request before they read what the
  store holds for it.** radosgw reads stored state before its permission
  check on these writes too. `rgw_build_bucket_policies` refuses a storage
  class the bucket's placement lacks (`rgw_op.cc:576-583` at v19.2.6,
  `:606-613` at v20.2.4). For PutObject, `RGWPutObj::init_processing`
  refuses a public canned ACL under the bucket's block of public ACLs
  (`:3903-3909`; `:4112-4118`), and its `get_params` looks up the grantees
  of the `x-amz-grant-*` headers and refuses a retention or a legal hold on
  a bucket without object lock (`rgw_rest_s3.cc:2618-2620` and
  `:2669-2673`; `:2779-2781` and `:2830-2834`). For CopyObject,
  `RGWCopyObj::init_processing` loads the source's bucket, a missing one
  refused (`rgw_op.cc:5392-5400`; `:5958-5966`), and
  `RGWCopyObj::verify_permission` reads the source, answers a missing one
  by the missing-object rule, decodes its ACL and refuses a copy onto
  itself that changes nothing, all before it checks the destination
  (`:5413-5462` and `:5487-5490`; `:5979-6028` and `:6053-6056`). So a
  refused requester learns from radosgw whether the bucket has such a
  storage class, a block of public ACLs or object lock, whether a named
  user or account exists, whether a copy source's bucket or key exists,
  whether its stored ACL is damaged, and whether its storage class is the
  one a copy onto itself names. rgw-go refuses these writes before the
  permission check only for what the request itself shows, its headers,
  arguments and declared length, and for the destination bucket's
  existence, which authorization needs. It reads a copy's source only
  once the destination allows the request and the requester's op mask
  allows the write, and makes every other check once the requester is
  authorized, so a requester the destination refuses, or whose op mask
  refuses the write, gets 403 AccessDenied whatever the buckets, the users
  or the source hold. For the op mask this differs from radosgw on a
  missing source bucket: radosgw loads it in `init_processing`, before
  `verify_op_mask` (`rgw_process.cc:202` and `:208` at v19.2.6 and
  v20.2.4), and answers 404 NoSuchBucket. The destination's storage class
  is checked before the source is read, as `init_permissions` checks it.
  The checks of stored state keep radosgw's order among themselves, but
  each now follows the request's own refusals: an authorized requester
  sending a `Content-MD5` that does not decode and a storage class the
  placement lacks is answered 400 InvalidDigest, where radosgw answers 400
  InvalidArgument for the storage class, and one sending an
  `x-amz-server-side-encryption` header with a public canned ACL under a
  block is answered 501 NotImplemented, where radosgw answers 403
  AccessDenied. rgw-go serves no POST-object: it answers every
  authenticated requester 501 NotImplemented without reading the bucket,
  once a failed authentication and a suspended user have been refused.
- **Request headers stored as attrs follow Tentacle's blocklist, and the
  session token is never stored.** radosgw stores every `x-amz-` header,
  and those under its other meta prefixes, as an attr of the object a
  PutObject or a CopyObject with REPLACE writes, except those its blocklist
  names (`rgw_get_request_metadata`, `rgw_op.h:2171-2233` at v19.2.6,
  `:2338-2408` at v20.2.4). rgw-go stores the `X-Amz-` headers alone, none
  under the other prefixes ("The S3 API reads X-Amz- headers only"), and
  applies v20.2.4's list on both releases, so on a Squid zone it stores no
  `x-amz-content-sha256`, `x-amz-checksum-algorithm`, `x-amz-date` or
  copy-source SSE-C header, which v19.2.6 stores, and on both it also
  leaves out `x-amz-security-token`, which both store
  (`docs/ceph-upstream-bugs.md`, "[radosgw stores request credentials as
  object
  attrs](ceph-upstream-bugs.md#radosgw-stores-request-credentials-as-object-attrs)").
  GET and HEAD show only the `x-amz-meta-` attrs, so a client sees no
  difference from the blocklist; one that sends `X-Goog-Meta-*` sees no
  metadata where radosgw shows it.
- **A date real_time cannot hold is refused.** radosgw keeps a retain-until
  date as a `ceph::real_time`, whose `timespan` is a u64 `rep` of
  nanoseconds since the epoch, so its last instant is
  2554-07-21T23:34:33.709551615Z
  ([ceph_time.h:66-74](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_time.h#L66-L74),
  [ceph_time.h:63-71](https://github.com/ceph/ceph/blob/v20.2.4/src/common/ceph_time.h#L63-L71)).
  `from_iso_8601` reads the date, and its `calculate` converts the time
  parsed modulo 2^64 nanoseconds
  ([iso_8601.cc:39-48](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L39-L48),
  [iso_8601.cc:39-48](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.cc#L39-L48)),
  though the header that declares `from_iso_8601` requires every time to
  fit
  ([iso_8601.h:14-33](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.h#L14-L33),
  [iso_8601.h:14-33](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.h#L14-L33)):
  `3000-01-01T00:00:00Z` reads as 2415-06-13, `2600-01-01T00:00:00Z` as
  2015-06-13, and `1970-01-00`, a day 00 in 1970 that it lets through, as
  2554-07-20 (`docs/ceph-upstream-bugs.md`, "[radosgw's object-lock dates
  wrap past
  2554](ceph-upstream-bugs.md#radosgws-object-lock-dates-wrap-past-2554)").
  So PutObject, CopyObject and CreateMultipartUpload take an
  `x-amz-object-lock-retain-until-date` that wraps to a date after the
  clock's and lock the object until the wrapped date, and refuse one that
  wraps to an earlier date with 400 InvalidArgument
  ([rgw_rest_s3.cc:2652-2663](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2652-L2663),
  [rgw_rest_s3.cc:2805-2816](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2805-L2816);
  [rgw_rest_s3.cc:3515-3526](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3515-L3526),
  [rgw_rest_s3.cc:3765-3776](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3765-L3776);
  [rgw_rest_s3.cc:4030-4039](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4030-L4039),
  [rgw_rest_s3.cc:4481-4490](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4481-L4490)).
  rgw-go's reader, `iso8601.Parse`, refuses every time before the epoch or
  past that instant, so each such header answers 400 InvalidArgument
  (`objectLockHeaders`, `internal/s3/putobject.go`). A date inside the
  range reads as radosgw reads it, to the nanosecond.

  The same reader refuses such a `RetainUntilDate` in a PutObjectRetention
  body as 400 MalformedXML (`objectlock.ParseRetention`). radosgw reads it
  wrapped and answers by where the wrap lands: before the clock's second,
  400 InvalidArgument, "the retain-until date must be in the future"
  ([rgw_op.cc:8387-8392](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8387-L8392),
  [rgw_op.cc:9322-9327](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9322-L9327));
  earlier than a retention in force, 403 AccessDenied
  ([rgw_op.cc:8413-8418](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8413-L8418),
  [rgw_op.cc:9348-9353](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9348-L9353));
  and otherwise it stores the wrapped date. And the default retention a bucket's rule
  gives each object written to it, radosgw's `get_lock_until_date`, adds
  the rule's days or years to the mtime in the same u64 and wraps
  ([rgw_object_lock.cc:103-111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L103-L111),
  [rgw_object_lock.cc:87-95](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L87-L95)):
  a rule of `Years` 528, written in October 2026, locks each new object
  until 1970-04-01, so not at all, and one of `Years` 600 until 2042.
  `objectlock.LockUntil` clamps the date to the last instant, which radosgw
  reads back as a lock until 2554, and to the epoch for a negative period
  that reaches before it, which PutObjectLockConfiguration never stores.
  `objectlock.PeriodFits` tells whether a rule's period
  from now ends inside the range, for PutObjectLockConfiguration to refuse
  one that does not as 400 InvalidRetentionPeriod, where radosgw stores it.
- **An object-lock attr with bytes past its encoding does not decode.**
  radosgw decodes a version's `user.rgw.object-retention` and
  `user.rgw.object-legal-hold` in two ways. Four callers use the convenience
  overload `decode(T&, const bufferlist&)`, which after the struct's decode
  asserts that it read every byte
  ([encoding.h:631-637](https://github.com/ceph/ceph/blob/v19.2.7/src/include/encoding.h#L631-L637),
  [encoding.h:632-638](https://github.com/ceph/ceph/blob/v20.2.4/src/include/encoding.h#L632-L638)),
  ending the process on bytes past the encoding. Only an attr too short to
  decode reaches the caller's `catch`, and each caller answers it its own
  way. A delete's `verify_object_lock` returns -EIO
  ([rgw_common.cc:1639-1667](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1639-L1667),
  [rgw_common.cc:1702-1730](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1702-L1730)),
  and so does PutObjectRetention's read of the existing retention
  ([rgw_op.cc:8403-8412](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8403-L8412),
  [rgw_op.cc:9338-9347](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9338-L9347)).
  Lifecycle's `pass_object_lock_check` returns false, so the version is not
  expired
  ([rgw_lc.cc:318-345](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L318-L345),
  [rgw_lc.cc:318-345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L318-L345)).
  The lock headers of GET and HEAD log the error and leave the header out
  of a response that otherwise succeeds
  ([rgw_rest_s3.cc:603-621](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L603-L621),
  [rgw_rest_s3.cc:712-730](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L712-L730)).
  GetObjectRetention and GetObjectLegalHold decode through an iterator,
  `obj_retention.decode(iter)` and `obj_legal_hold.decode(iter)`, which
  leaves such bytes unread
  ([rgw_op.cc:8475-8482](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8475-L8482),
  [rgw_op.cc:9423-9430](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9423-L9430);
  [rgw_op.cc:8579-8586](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8579-L8586),
  [rgw_op.cc:9547-9554](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9547-L9554))
  (`docs/ceph-upstream-bugs.md`, "[radosgw aborts on an object-lock attr
  with bytes past its
  encoding](ceph-upstream-bugs.md#radosgw-aborts-on-an-object-lock-attr-with-bytes-past-its-encoding)").
  rgw-go's strict `objectlock.DecodeRetention` and
  `objectlock.DecodeLegalHold`, for the four, take such an attr for one that
  does not decode, so `objectlock.VerifyDelete` refuses the delete with the
  error radosgw gives a short attr, -EIO, and PutObjectRetention
  (`op.PutObjectRetention`) answers such an existing retention as radosgw
  answers a short one, 500 UnknownError. The lenient
  `objectlock.DecodeRetentionLenient` and `objectlock.DecodeLegalHoldLenient`
  read it as the two Get ops do, so GetObjectRetention and GetObjectLegalHold
  (`op.GetObjectRetention`, `op.GetObjectLegalHold`) answer no 500 for it,
  only for an attr too short to decode. radosgw writes these attrs cleanly, and
  copy_obj takes them from the request, not the source
  ([rgw_rados.cc:4800-4807](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4800-L4807),
  [rgw_rados.cc:5013-5020](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5013-L5020)),
  so only corruption or a writer with RADOS access leaves such bytes.
- **A lapsed retention binds no retention change.** radosgw's
  PutObjectRetention checks the version's existing retention whatever its
  date: a proposed retain-until date earlier than the existing one needs
  the existing mode GOVERNANCE and the bypass, and COMPLIANCE cannot become
  GOVERNANCE
  ([rgw_op.cc:8413-8429](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8413-L8429),
  [rgw_op.cc:9348-9364](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9348-L9364)).
  So a version whose COMPLIANCE retention has lapsed can never be given a
  GOVERNANCE retention, nor one ending before the lapsed date, though the
  lapsed retention protects nothing: radosgw's `verify_object_lock` lets
  a delete through once the date's second is not after now's
  ([rgw_common.cc:1649-1653](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1649-L1653),
  [rgw_common.cc:1712-1716](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1712-L1716))
  (`docs/ceph-upstream-bugs.md`, "[radosgw's PutObjectRetention holds a
  lapsed retention's mode and
  date](ceph-upstream-bugs.md#radosgws-putobjectretention-holds-a-lapsed-retentions-mode-and-date)").
  rgw-go's PutObjectRetention (`op.PutObjectRetention`) checks the change
  with `objectlock.RetentionChange`, which takes an existing retention whose
  retain-until second is not after now's for none, so the new retention is
  checked as on a version without one; one still in force is checked as
  radosgw checks it, in whole seconds. The retention then stored is the
  RGWObjectRetention radosgw stores for the same document on a version
  without one.
- **A retain-until second is compared with the clock exactly.** radosgw
  compares a retain-until date's whole seconds, `to_time_t`, with
  `ceph_clock_now()`, a `utime_t`, which C++ converts through `utime_t`'s
  `operator double`
  ([utime.h:230-232](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L230-L232),
  [utime.h:230-232](https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L230-L232)).
  It does so at six sites. A delete's `verify_object_lock`
  ([rgw_common.cc:1649-1653](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1649-L1653),
  [rgw_common.cc:1712-1716](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1712-L1716))
  and lifecycle's `pass_object_lock_check`
  ([rgw_lc.cc:328-329](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L328-L329),
  [rgw_lc.cc:328-329](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L328-L329))
  hold a version while the date's second is after the clock. The checks
  that an `x-amz-object-lock-retain-until-date` is in the future refuse a
  date whose second is not after the clock: PutObject's
  `RGWPutObj_ObjStore_S3::get_params`
  ([rgw_rest_s3.cc:2652-2656](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2652-L2656),
  [rgw_rest_s3.cc:2805-2809](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2805-L2809)),
  CopyObject's `RGWCopyObj_ObjStore_S3::get_params`
  ([rgw_rest_s3.cc:3515-3519](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3515-L3519),
  [rgw_rest_s3.cc:3765-3769](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3765-L3769))
  and CreateMultipartUpload's `RGWInitMultipart_ObjStore_S3::get_params`
  ([rgw_rest_s3.cc:4030-4033](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4030-L4033),
  [rgw_rest_s3.cc:4481-4484](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4481-L4484)).
  PutObjectRetention's check of its body's date, in
  `RGWPutObjRetention::execute`, refuses one whose second is before the
  clock
  ([rgw_op.cc:8387-8392](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8387-L8392),
  [rgw_op.cc:9322-9327](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9322-L9327)).
  At today's clock a double holds the fraction of a second to 2^-22 s, so
  a clock within about 119 ns of a whole second reads as that second:
  radosgw then lets a delete or an expiry through a retention ending at
  that second, refuses a header date of that second, and takes a body date
  of that second at a clock just past it. rgw-go compares the clock's whole
  seconds exactly (`objectlock.VerifyDelete`, for a delete and for
  lifecycle; `objectLockHeaders` in `internal/s3/putobject.go`, for the
  three headers; `objectlock.RetentionChange`; `op.PutObjectRetention`, for
  its body's date), so in that window it keeps the version, takes the header
  date, and refuses the body date.
- **CopyObject refuses an object lock on a bucket without object lock.**
  radosgw's CopyObject stores the retention and legal hold its headers ask
  for on a bucket without object lock, where its PutObject refuses them
  with 400 InvalidRequest (`docs/ceph-upstream-bugs.md`, "[radosgw's
  CopyObject stores an object lock on a bucket without object
  lock](ceph-upstream-bugs.md#radosgws-copyobject-stores-an-object-lock-on-a-bucket-without-object-lock)").
  rgw-go refuses the copy as the PutObject is refused, once the requester
  is authorized. On a bucket with object lock both writes answer 501
  ("Writes to a versioned or object-lock bucket answer 501 until
  versioning is served").
- **No x-amz-expiration on a write.** radosgw sends `x-amz-expiration`
  from the bucket's lifecycle rules on a PutObject's success too
  (`get_s3_expiration_header`, `rgw_rest_s3.cc:2742` at v19.2.6, `:2903`
  at v20.2.4). rgw-go reads no lifecycle configuration until phase 2 ("No
  x-amz-expiration", above).
- **A date whose time names a conversion rgw-go does not run is refused.**
  radosgw reads DeleteObject's `x-amz-delete-if-unmodified-since` and the
  admin API's date arguments, such as the usage API's `start` and `end`,
  with `utime_t::parse_date`, which reads the time after the date with a
  strptime format built from the value's own text
  (`include/utime.h:406-441` at v19.2.6 and v20.2.4). A `%` there is a
  conversion glibc's strptime applies to the rest of the value, and a
  value that reaches the format's 31st byte with a sign also makes radosgw
  write past the format's end (`docs/ceph-upstream-bugs.md`, "[radosgw's
  parse_date writes a NUL one byte past its format
  buffer](ceph-upstream-bugs.md#radosgws-parse_date-writes-a-nul-one-byte-past-its-format-buffer)");
  rgw-go parses that value as radosgw's format then reads it.
  - An admin date: rgw-go runs `%a %A %b %B %h %d %e %H %m %M %S %y %Y %z
    %n %t %%` as glibc does (`internal/strptime`). It answers 400
    InvalidArgument for any other character after a `%`, including a flag,
    width or E/O modifier on a listed conversion, such as `%-d`, `%2S`,
    `%Od` or `%EY`. glibc runs those modified forms and `%C %D %F %G %I %R
    %T %U %V %W %X %Z %c %g %j %k %l %p %r %s %u %w %x`, so radosgw reads
    such a value; a character glibc does not know either, such as `%q` or
    a trailing `%`, is refused by both. The usage show reads every date it
    cannot parse as the argument's default instead ("The admin usage
    routes give a date they cannot parse a value of their own").
  - A delete date: rgw-go answers a value with any `%` after its seconds
    with 400 InvalidArgument (`internal/op/deleteobject.go`); radosgw
    parses it as that conversion directs.
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
- **CompleteMultipartUpload's Location finds its domain without CNAME
  lookups.** rgw-go builds the Location as `compute_domain_uri` does, with
  no scheme under a configured name
  ([rgw_rest.h:805-818](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L805-L818),
  [rgw_rest.h:817-830](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L817-L830);
  `docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload
  Location has no scheme under a configured
  domain](ceph-upstream-bugs.md#radosgws-completemultipartupload-location-has-no-scheme-under-a-configured-domain)"),
  and takes the domain from `rgw_dns_name` and the zonegroup's hostnames.
  Neither gateway builds a Location under a website hostname: with the
  website API enabled a Host in one makes a website request, and the
  website handler's `op_post` is NULL
  ([rgw_rest_s3website.h:34](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3website.h#L34),
  [rgw_rest_s3website.h:34](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3website.h#L34)),
  which `process_request` answers 405 MethodNotAllowed
  ([rgw_process.cc:325-329](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L325-L329),
  [rgw_process.cc:327-331](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L331)),
  as rgw-go's website dispatch answers it. radosgw also takes the domain
  from the name `rgw_resolve_cname` resolves the Host to
  ([rgw_rest.cc:2091-2130](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2091-L2130),
  [rgw_rest.cc:2103-2142](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2103-L2142)),
  where rgw-go writes `rgw_dns_name`, or the scheme and the Host. An
  HTTP/1.0 request that sends its Host empty gets `<HTTP_HOST>`, as one
  that sends none does: net/http does not tell them apart.
- **A multipart upload with a retention or a legal hold on a bucket with
  object lock answers 501.** radosgw keeps the object-lock headers of
  CreateMultipartUpload and UploadPart on the upload and the parts' heads.
  rgw-go serves no object lock until versioning is served, so it refuses a
  request on such a bucket naming `x-amz-object-lock-mode` or
  `x-amz-object-lock-legal-hold` with 501 NotImplemented once the
  requester is authorized, where PutObject refuses such a bucket. The
  headers' values are checked as PutObject checks them, before the
  permission check on UploadPart, as its `get_params` runs in
  `init_processing`, and once the requester is authorized on
  CreateMultipartUpload, as its own runs in `execute`. Their refusal on a
  bucket without object lock, which reads the bucket, comes once the
  requester is authorized on both ("The multipart routes authorize a
  request before they read what the store holds for it").
- **UploadPart ignores If-Match and If-None-Match.** rgw-go writes a part
  head without the request's write conditions on both releases, as
  v19.2.7's `MultipartObjectProcessor::complete` does, which still passes
  none
  ([rgw_putobj_processor.cc:491-531](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L491-L531)).
  v20.2.4's passes them to the part head's write
  ([rgw_putobj_processor.cc:563-564](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L563-L564)), where
  they are checked against the part head the request has just created:
  If-None-Match: `*`, and If-Match naming an ETag, then fail with 412
  PreconditionFailed for a part in the bucket placement's pool, and any
  If-Match with 404 NoSuchKey for a part whose storage class lives in
  another pool, after the body was received (`docs/ceph-upstream-bugs.md`,
  "[Tentacle fails an UploadPart that sends If-None-Match: * for a part in
  the bucket placement's pool](ceph-upstream-bugs.md#tentacle-fails-an-uploadpart-that-sends-if-none-match--for-a-part-in-the-bucket-placements-pool)").
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
- **UploadPartCopy checks the copy-source conditions.** radosgw reads
  `x-amz-copy-source-if-match`, `-if-none-match`, `-if-modified-since` and
  `-if-unmodified-since` only for CopyObject
  (`RGWCopyObj_ObjStore_S3::get_params`, `rgw_rest_s3.cc:3511-3514` at
  v19.2.6, `:3791-3794` at v20.2.4) and ignores them on UploadPartCopy, so a
  part copies whatever version the source holds. rgw-go checks them on
  UploadPartCopy as it checks them on CopyObject, against the one state it
  copies: a failing If-Match or If-Unmodified-Since answers 412
  PreconditionFailed, a matching If-None-Match or an If-Modified-Since the
  source has not passed answers 304 NotModified, and a date `parse_time`
  does not read answers 400 InvalidArgument, each before anything is
  copied, where radosgw copies the part.
- **A copy-source range past 2^63 answers 416.** radosgw holds
  `x-amz-copy-source-range`'s bounds in an `off_t` (`rgw_op.h:1227-1228` at
  v19.2.6, `:1297-1298` at v20.2.4), so a bound of 2^63 or more turns
  negative. A first bound that turns negative passes the check that it is
  not past the last unless the last is lower still, and range_to_ofs then
  reads the negative offset as a suffix from the source's end, clamped to
  its first byte, with the source's last byte as the end
  (`rgw_sal.cc:429-448` at v19.2.6, `:426-445` at v20.2.4); radosgw's copy
  of such a range is a resource-exhaustion defect
  (`docs/ceph-upstream-bugs.md`, "[radosgw
  holds a copy-source range's bounds in an off_t, so a bound past 2^63
  reads as a
  suffix](ceph-upstream-bugs.md#radosgw-holds-a-copy-source-ranges-bounds-in-an-off_t-so-a-bound-past-263-reads-as-a-suffix)").
  rgw-go refuses such a range with 416 InvalidRange before it authorizes,
  as a range that starts past any object. A range radosgw's check refuses,
  a last bound that turns negative under a first that does not among them,
  answers 416 InvalidRange from both.
- **An upload id holding a "." is NoSuchUpload.** radosgw names an
  upload's meta object `<key>.<upload id>.meta` and its part prefix
  `<key>.<upload id>` without checking the id (`RGWMPObj::init`,
  `services/svc_tier_rados.h:42-55` at v19.2.6 and v20.2.4), so key `a`
  with upload id `b.2~X` addresses key `a.b`'s upload `2~X`, while the
  request is authorized as key `a` (`docs/ceph-upstream-bugs.md`,
  "[radosgw lets an upload id address another key's multipart upload](ceph-upstream-bugs.md#radosgw-lets-an-upload-id-address-another-keys-multipart-upload)").
  rgw-go answers UploadPart, UploadPartCopy, CompleteMultipartUpload,
  AbortMultipartUpload and ListParts naming such an id as it answers a
  missing upload, at the same point and without reading the store: 404
  NoSuchUpload, and for ListParts the missing-object rule. No gateway makes
  such an id: radosgw's and rgw-go's are `2~` and gen_rand_alphanumeric's
  `A-Za-z0-9-_` (`driver/rados/rgw_sal_rados.cc:3281-3283` at v19.2.6,
  `:4125-4127` at v20.2.4; `common/random_string.cc:48`).
- **ListParts reads the upload before it authorizes.** radosgw reads only
  the meta object's ACL before verify_permission (`read_obj_policy`,
  `rgw_op.cc:398-447` at v19.2.6, `:428-477` at v20.2.4) and decodes the
  upload's info in execute, where an empty meta object is NoSuchUpload and
  one that does not decode is -EIO. rgw-go reads and decodes the upload
  first, so a meta object whose info does not decode answers 500
  UnknownError to every requester, where radosgw answers a refused one 403
  AccessDenied, and an empty meta object takes the missing-object rule,
  404 NoSuchKey or 403 AccessDenied, where radosgw authorizes against its
  ACL and then answers 404 NoSuchUpload. Only a corrupt upload meets
  either, and rgw-go lists nothing for it.
- **ListParts names the owner of an ACL whose grants do not decode.**
  radosgw's ListParts decodes the meta object's whole ACL and answers 500
  UnknownError, -EIO, when it does not decode (`RGWListMultipart::execute`,
  `rgw_op.cc:6680-6688` at v19.2.6, `:7604-7612` at v20.2.4). rgw-go reads
  the owner alone, as `decode_policy` does for the index entries
  `set_attrs` and the listing's reconciliation write, so it lists the parts
  under the owner a policy names even when its grants do not decode, and
  under no owner when its owner does not. No S3 op writes such an ACL.
- **UploadPartCopy refuses a cloud-tiered source with a range too.**
  radosgw refuses a source transitioned to a cloud tier with 403
  InvalidObjectState only for a copy without `x-amz-copy-source-range`
  (`rgw_op.cc:4297-4326` at v19.2.6, `:4510-4538` at v20.2.4), and reads a
  ranged copy's source without that check. rgw-go refuses both.
- **The multipart ops after UploadPart check no storage class.** radosgw
  reads `x-amz-storage-class` for every S3 request
  (`rgw_rest_s3.cc:5043-5046` at v19.2.6, `:5603-5606` at v20.2.4) and,
  while it loads the bucket, refuses one whose class the bucket's
  placement lacks with 400 InvalidArgument (`rgw_op.cc:576-583` at v19.2.6,
  `:606-613` at v20.2.4). rgw-go makes that check on CreateMultipartUpload,
  whose upload keeps the class, and on UploadPart and UploadPartCopy, whose
  part keeps the upload's, and not on CompleteMultipartUpload,
  AbortMultipartUpload, ListParts or ListMultipartUploads, which take their
  placement from the upload or need none, so it serves such a request
  where radosgw refuses it.
- **An upload without a v2 id keys a part by its canonical number.**
  radosgw registers a part of an upload whose id lacks the `2~` or `2/`
  prefix under `part.` and the part number as the request spelled it
  (`part_num_str`, `driver/rados/rgw_putobj_processor.cc:534-543` at
  v19.2.6, `:572-581` at v20.2.4). The driver receives the number as an
  integer and writes it in decimal without leading zeros, so a request
  spelling it `07` registers `part.7` where radosgw registers `part.07`.
  Only an upload that a radosgw older than the v2 ids created reaches
  this; rgw-go and every current radosgw make v2 ids.
- **CompleteMultipartUpload checks the part heads and keeps the parts until
  its head is written.** rgw-go validates and assembles a completion as
  `RadosMultipartUpload::complete` does (`driver/rados/rgw_sal_rados.cc:3433-3640`
  at v19.2.6, `:4279-4490` at v20.2.4), with these differences:
  - Before the head write, rgw-go stats the head object of every part and
    answers 400 InvalidPart, leaving the upload in place, when one is
    missing. radosgw checks only the part infos the meta object lists, so it
    writes a head naming parts whose objects are gone, which reads as
    NoSuchKey.
  - A stored part ETag that is not 32 hex digits answers 400 InvalidPart.
    radosgw's `hex_to_buf` hashes whatever it decodes of it into the
    object's ETag (`:3503-3505` at v19.2.6, `:4351-4353` at v20.2.4). Every
    gateway stores a part's ETag as the hex MD5 of its body, so only a
    damaged part info reaches this.
  - radosgw sends the parts' index entries, and those of each part's
    earlier uploads, with the head write's index change
    (`obj_op.meta.remove_objs`, `:3624` at v19.2.6, `:4472` at v20.2.4), and
    its cancel of a refused head write applies them too
    (`rgw_rados.cc:3374` at v19.2.6, `:3527` at v20.2.4;
    `cls/rgw/cls_rgw.cc:1213-1227` at v19.2.6, `:1344-1358` at v20.2.4): a
    completion refused by If-None-Match: * or If-Match: * losing its race,
    or failing at the OSD, leaves an upload whose parts have no index
    entries (`docs/ceph-upstream-bugs.md`, "[A refused CompleteMultipartUpload
    drops its parts' index
    entries](ceph-upstream-bugs.md#a-refused-completemultipartupload-drops-its-parts-index-entries)").
    rgw-go sends them with the meta object's removal, after the head is
    written, so a refused completion keeps them, and the bucket's stats
    count the parts with the object for the moment between the two.
  - radosgw queues each part's earlier uploads for the GC before it writes
    the head, one GC entry per such part under the upload id
    (`cleanup_part_history`, `:3103-3148` and `:3573` at v19.2.6,
    `:3947-3992` and `:4421` at v20.2.4); rgw-go queues them in one entry
    after the head write. A refused completion keeps them, and a crash or a
    timeout between the head write and the meta object's removal leaves the
    upload's objects and entries for a retry to retire.
- **CompleteMultipartUpload finishes an earlier completion of its upload
  instead of writing it again.** When the key's head names an object of the
  upload being completed, an earlier completion wrote it and its meta object
  outlived it: the process stopped, or the meta object's removal failed,
  which radosgw only logs (`rgw_op.cc:6497-6530` at v19.2.6, `:7387-7420` at
  v20.2.4). radosgw completes such an upload again, and its head write
  queues the earlier head's tails, the same parts, for the GC, so the object
  loses its data; an abort does the same (`docs/ceph-upstream-bugs.md`, "[A
  multipart completion whose meta object outlives its head write lets a
  retry or an abort delete the object's
  data](ceph-upstream-bugs.md#a-multipart-completion-whose-meta-object-outlives-its-head-write-lets-a-retry-or-an-abort-delete-the-objects-data)").
  rgw-go's own completions leave a record on the meta object, which decides
  a retry ("A CompleteMultipartUpload records itself on the meta object
  before its head write"). For a meta object without one, as a floor
  radosgw leaves it, rgw-go reads the key's current version, its OLH
  followed as a read follows it, and, in a bucket whose versioning was ever
  enabled, each other version of the key whose ETag is a multipart one,
  listed from the key's index shard newest first, before writing. The
  listing stops at the first entry written more than 15 minutes, the
  request clock skew S3 allows, before the earlier of two times: the mtime
  of the meta object's index entry, which ListMultipartUploads reports as
  the upload's Initiated, and the earliest Modified among the upload's part
  records in the meta object's omap. The meta object's own mtime moves with
  every part, lock and record written on it, and radosgw's listing repair,
  `check_disk_state`, copies it into a meta entry that has a pending op
  (`driver/rados/rgw_rados.cc:10583` and `:10638` at v19.2.7, `:11311` and
  `:11366` at v20.2.4), as a meta removal that timed out leaves one; no
  lock and no repair rewrites a part record. A version that names the
  upload's parts was written by a completion of the upload, after its
  initiation and its parts, so a version of a floor radosgw written more
  than 15 minutes before that time is not found, which no correct clock
  produces, and neither is one behind an entry that old, a version linked
  after this upload's completion whose own write came more than 15 minutes
  before the upload began. Nor is one whose parts were all uploaded again
  after it was written once a listing repair moved the meta entry's mtime,
  as the part records then carry the later uploads' times. The key's null
  version is then read on its own, its instance entry and, when its ETag
  is a multipart one, its head: a null version written over an earlier one
  and never linked keeps that one's list entry, at its epoch and with its
  metadata, which the listing orders and shows as the earlier version's.
  When the meta object's index entry is missing, nothing bounds the
  listing. The first version that names an object of the upload decides:
  - when it is the object this completion would write, with the same
    ETag, size and part objects, rgw-go writes no head, removes the meta
    object with the parts' entries, and answers 200 with the ETag and that
    version's id, without checking the request's If-Match or If-None-Match
    again. A version with an instance is linked already, since the current
    version's read finds only its key's OLH target and the listing only list
    entries, and it is not linked again. The null version, however it was
    found, is first linked when no link of it landed, as a replay links a
    recorded one ("A CompleteMultipartUpload records itself on the meta
    object before its head write"), and one that cannot be linked answers
    500 InternalError with the upload kept. Its link is judged by its data,
    not by its tag: an attr change rewrites the head's idtag and the
    instance entry's tag but not the list entry's, at both gateways
    ([rgw_rados.cc:6853-6860](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6853-L6860)
    and
    [rgw_rados.cc:6909](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6909)
    at v19.2.7,
    [rgw_rados.cc:7467-7474](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7467-L7474)
    and
    [rgw_rados.cc:7559](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7559)
    at v20.2.4; `SetObjectAttrs`, `internal/driver/attrs.go`). rgw-go takes
    the null version for unlinked only when the list entry at its instance
    entry's versioned epoch differs from that entry in ETag or size, which
    makes the list entry an earlier null version's. A null version written
    over an earlier one with the same ETag and size and never linked
    therefore reads as linked, and is answered 200 unlinked;
  - when it names the upload's parts but differs, rgw-go answers 404
    NoSuchUpload and changes nothing;
  - when rgw-go finishes the completion, in the first case or as a record
    lets it ("A CompleteMultipartUpload records itself on the meta object
    before its head write"), it compares no checksum the request supplies
    and answers with the one the head stores (`completeLocked` and
    `replayRecorded`, `internal/driver/mp_complete.go`). A Tentacle
    radosgw's retry of such an upload with a checksum type combines the
    parts and compares a supplied value on every pass that takes the lock,
    before `upload->complete`
    ([rgw_op.cc:7273-7336](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7273-L7336),
    [rgw_op.cc:7366-7369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7366-L7369)),
    so it can answer 400 BadDigest where rgw-go answers 200. Squid's
    radosgw compares no checksum between the lock and `upload->complete`
    ([rgw_op.cc:6438-6488](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6438-L6488)),
    and a Squid upload has no checksum type, so on Squid nothing differs.

  A completed object that a floor radosgw wrote and that was overwritten or
  deleted before the retry no longer names the upload, and leaves no
  record; its tails, the parts, are then queued for the GC, and rgw-go,
  like radosgw, completes the upload over them unless the GC has already
  removed a part head. A completed version that a later write made
  noncurrent still names the upload, and rgw-go finishes or refuses the
  completion as above, where radosgw, which reads the current version
  alone, completes the upload again into a second version naming the same
  parts, and deleting either then loses the other's data.
- **CompleteMultipartUpload renews its lock before the head write.** rgw-go
  takes the `RGWCompleteMultipart` lock as radosgw does, and just before
  the head write renews it, with `LOCK_FLAG_MUST_RENEW` and assert_exists in
  one op, for another `rgw_mp_lock_max_time`. When the lock is no longer its
  own, because it lapsed during a long completion or another gateway took
  it, rgw-go answers 500 InternalError, "This multipart completion is
  already in progress", writes nothing and leaves the upload whole.
  v19.2.6 and v20.2.4 neither renew nor check the lock, so a completion
  that outlives it can race another one over the same parts
  (`docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload does
  not keep its lock past rgw_mp_lock_max_time](ceph-upstream-bugs.md#radosgws-completemultipartupload-does-not-keep-its-lock-past-rgw_mp_lock_max_time)");
  later radosgw renews it every half duration and refuses the head write
  with the same 500 when a renewal failed
  ([ceph/ceph#67696](https://github.com/ceph/ceph/pull/67696)). Requests
  of one gateway share its RADOS client, as radosgw's do, but each
  completion locks and renews under a cookie of its own ("Lock names are
  radosgw's; cookies are not shared"), so the renewal fails too when
  another completion of the same gateway took a lapsed lock, where
  radosgw's renewal under "" would pass. The renewal also leaves a
  window from the read of the key's head that looks for an earlier
  completion to the head write's own read: a completion of the same upload
  whose head write was in flight longer than `rgw_mp_lock_max_time`, after
  its renewal, can land its head there, and this completion's head write
  then queues that head's tails, the same parts, for the GC. radosgw main
  has the same window.
- **AbortMultipartUpload removes the upload before it frees the parts.**
  rgw-go aborts an upload as `RadosMultipartUpload::abort` does
  (`driver/rados/rgw_sal_rados.cc:3151-3263` at v19.2.6, `:3995-4107` at
  v20.2.4), under the `RGWCompleteMultipart` lock that
  `RGWAbortMultipart::execute` takes (`rgw_op.cc:6614-6650` at v19.2.6,
  `:7538-7574` at v20.2.4), with these differences:
  - radosgw queues every part's objects for the GC and then removes the
    meta object with the parts' index entries; a removal that fails leaves
    an upload that can still be completed over the queued objects, and
    its cancel drops the parts' entries (`docs/ceph-upstream-bugs.md`,
    "[radosgw's AbortMultipartUpload queues the parts for the GC before it removes the upload](ceph-upstream-bugs.md#radosgws-abortmultipartupload-queues-the-parts-for-the-gc-before-it-removes-the-upload)"). rgw-go removes the meta object
    first and queues the parts, in one chain under the upload id, once it
    is gone. A removal that fails or that racing parts cancel fifteen times
    leaves the upload whole, its parts' entries included, and queues
    nothing; one that times out queues nothing either, and its parts leak
    when it lands. A gateway that stops between the removal and the queue
    leaks the parts, where radosgw's leaves a live upload over freed ones.
  - rgw-go takes the lock under a cookie of the abort's own, 128 random
    bits, where radosgw's serializer uses "" ("Lock names are radosgw's;
    cookies are not shared"), and the removal
    asserts, in its own op and before `obj_remove`, that the abort still
    holds it under that cookie. A cls_lock locker is the request's entity
    and its cookie (`cls/lock/cls_lock.cc:174-178`, `:201-212` and
    `:507-519` at v19.2.6 and v20.2.4), and requests of one gateway share
    its RADOS client and so its entity ("CompleteMultipartUpload renews its
    lock before the head write"), so the cookie is what tells the abort's
    hold from one a completion of the same gateway takes after the abort's
    lapsed. The OSD then refuses the removal, however late it runs, once
    the abort's hold lapsed or was broken, whoever took the lock since: the
    abort answers 503 ServiceUnavailable, or 408 when it stopped waiting,
    and queues nothing. A radosgw gateway is another entity, so the cookie
    changes nothing in how its locks and the abort's exclude each other.
    rgw-go also answers 503 early, without the op, once less than
    `min(30 s, rgw_mp_lock_max_time / 4)` of the lock's term remains, and
    waits no longer than that for the removal's reply. radosgw neither
    renews nor checks the lock.
  - A meta object another gateway removed meanwhile answers 404
    NoSuchUpload, as on radosgw, without the quota cache change and with
    nothing queued, where radosgw has already queued the parts.
  - Each abort reads the key's head as well ("AbortMultipartUpload leaves a
    completed upload's parts alone").
  - A part without a manifest, which only a gateway older than Hammer
    wrote, is logged and skipped, and its head's index entry under the
    upload's own prefix is retired with the meta object. radosgw's branch
    for it deletes an object with an empty name, because `list_parts`
    leaves the part's oid empty (`:3186-3192` at v19.2.6, `:4030-4036` at
    v20.2.4), and keeps that entry; the part's objects leak on both.
- **AbortMultipartUpload leaves a completed upload's parts alone.** An
  upload whose meta object outlived its completion's head write still
  lists its parts, and radosgw's abort queues them for the GC, the
  completed object's data (`docs/ceph-upstream-bugs.md`, "[A multipart
  completion whose meta object outlives its head write lets a retry or an
  abort delete the object's
  data](ceph-upstream-bugs.md#a-multipart-completion-whose-meta-object-outlives-its-head-write-lets-a-retry-or-an-abort-delete-the-objects-data)").
  rgw-go reads, in each round of the abort:
  - when the meta object carries a completion record ("A
    CompleteMultipartUpload records itself on the meta object before its
    head write"), the head the record names, the recorded version read
    without following the key's OLH, current or not; when it carries the
    recorded tag and is a completed object, an unversioned head, the null
    version, or a version whose link landed as the replay reads it, rgw-go
    removes the meta object alone, behind its version and with its own size
    off the quota cache, lists no part and queues nothing, and answers 204,
    as
    [ceph/ceph#72103](https://github.com/ceph/ceph/pull/72103)'s abort
    does at its head, 2c1db8239cc. The parts' index entries stay, as in the
    PR. A version whose link never landed, which a retry answered 500 or
    412, is removed first, its instance entry and then its head, its parts
    to the GC, and the abort goes on as for an upload never completed,
    where the PR's abort leaves that version unlisted with the upload's
    parts. It is removed only once no link of it can land: the key's OLH
    head carries no pending
    modification younger than `rgw_olh_pending_timeout_sec`, an hour by
    default, and the name's OLH log names no op on the version, both read
    before the version's index entries are. A versioned write inits its
    pending modification before it sends its link, and rgw-go's keeps it
    after a link whose outcome is unknown until the link's log entry is
    replayed or the modification expires, which `check_pending_olh_entries`
    takes for its failure; a link that landed wrote the version's list
    entry and its log entry in one class call. Until then the abort answers
    500 InternalError and changes nothing. radosgw's `set_olh` cancels its
    pending modification after any failed link, a timeout included
    (`driver/rados/rgw_rados.cc:8715-8717` at v19.2.7, `:9461-9463` at
    v20.2.4), so a radosgw link still in flight then is not seen by those
    reads, and neither is a completion of the upload that stalled past its
    lapsed lock and links its version later. The removal closes both. It
    takes the version's index entry before its head, where radosgw's
    `delete_obj` removes the head first, and guards the entry's removal:
    - an index entry the completion prepared and never completed is first
      completed from the head under the completion's tag, as a replay
      completes it: complete_op's DEL keeps an entry while another op is
      pending on it
      ([cls_rgw.cc:1127-1167](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1127-L1167)
      at v19.2.7,
      [cls_rgw.cc:1261-1299](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1261-L1299)
      at v20.2.4), the completion's own index completion landing late would
      then make it exist again, and `bucket_link_olh` links any entry it
      finds, failing only on a missing one
      ([cls_rgw.cc:1668-1688](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1668-L1688),
      [cls_rgw.cc:1809-1829](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1809-L1829));
    - rgw-go sends the index prepare DEL of the version and then reads its
      instance entry with `bi_get`, which answers its stored bytes. An op
      pending on it, but the abort's own prepare, that is older than the tag
      timeout is taken for dead and canceled with complete_op CANCEL under
      its tag, and the entry read again: that is the rule
      `dir_suggest_changes` applies before it changes an entry
      ([cls_rgw.cc:2283-2299](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2283-L2299),
      [cls_rgw.cc:2453-2469](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2453-L2469)),
      and nothing else expires an op on an unlinked version, which no listing
      reaches. The tag timeout is the class's: the index shard header's
      `tag_timeout`, else `rgw_pending_bucket_index_op_expiration`, else 120
      seconds, a zero going down the list
      ([cls_rgw.cc:2215-2227](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2215-L2227),
      [cls_rgw.cc:2385-2397](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2385-L2397)),
      where rgw-go reads the option from its own configuration and the class
      from the OSD's. The entry must then be the completion's, completed under
      its tag, or none but the one the prepare made, with no op pending on it
      but the prepare's and no versioned epoch, which every link sets
      ([cls_rgw.cc:1410-1427](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427),
      [cls_rgw.cc:1537-1554](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554));
      otherwise the prepare is canceled and the abort answers 500
      InternalError with nothing removed. An abort refused by another op
      pending on the version, the prepare of an abort that stopped before
      its removal among them, succeeds once that op is older than the tag
      timeout. An op's time is the OSD's clock at its prepare, which the abort
      compares with the gateway's own, so clock skew between the two moves
      that bound, as skew between the OSD's clocks moves the class's own rule;
      an op still in flight that the abort canceled then meets complete_op's
      EINVAL for a tag no entry holds;
    - in one op on the index shard it compares those bytes, OMAP_CMP, and
      completes the DEL, which removes the entry. A link that rewrote the
      entry since the read fails the whole op
      ([PrimaryLogPG.cc:7802-7875](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7802-L7875),
      [PrimaryLogPG.cc:7879-7952](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7879-L7952)),
      and the abort answers 500 with nothing removed. A link sent after it
      meets `bucket_link_olh`'s ENOENT, and the completion's index
      completion complete_op's EINVAL for a tag no entry holds
      ([cls_rgw.cc:1066-1073](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1066-L1073),
      [cls_rgw.cc:1201-1208](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1201-L1208));
    - only then is the head removed, guarded on the recorded tag, and its
      tails queued. An abort that stops between the two removals leaves a
      head no index entry names, which the next abort finds through the
      record and removes the same way. A head whose tag another write
      changed in between, as an attr change by version id does, is kept and
      the abort answers 500; the next abort takes that head for replaced,
      as any recorded head that no longer carries the tag, and queues the
      parts, which only the unlisted head names.

    So no link of the version, from either gateway and at any moment,
    leaves it listed without its head: a radosgw link in flight or a
    stalled completion costs a 500 at worst. The removal also runs only
    while the abort's lock has its margin left, checked just before it,
    and once an `assert_locked` of the abort's hold on the meta object
    succeeds, sent just before it, so an abort whose lock lapsed or was
    taken over by then removes nothing and answers 503 ServiceUnavailable.
    The assertion and the removal are ops on two objects, so a takeover can
    still come in the round trip between them; a link the new holder makes
    then meets the compare or the missing entry above, and cannot leave the
    index naming a version without its head. An unlinked null
    version keeps its parts and the abort removes the meta object alone: its
    head is the key's OLH head, which a failed null write also leaves;
  - otherwise the key's versions, as a completion without a record reads
    them ("CompleteMultipartUpload finishes an earlier completion of its
    upload instead of writing it again"), the current one and, in a bucket
    whose versioning was ever enabled, each noncurrent one written since
    15 minutes before the upload's initiation or its earliest part, and its
    null version: when one names an object of the upload, as a floor
    radosgw's completion leaves it,
    rgw-go answers 404 NoSuchUpload and changes nothing, where v19.2.6 and
    v20.2.4 queue the object's parts. The upload stays listed until that
    version is overwritten or deleted, which queues its tails, the parts,
    after which an abort succeeds.
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
  rgw-go also answers success (a copy onto itself excepted) and drops
  them, as `done_ret` would and as the upstream fix in review,
  [ceph/ceph#72098](https://github.com/ceph/ceph/pull/72098), does.
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
- **A copy onto its own key in a suspended bucket references the current
  version's tails.** On a bucket whose versioning is suspended, a copy of a
  key onto itself, which the S3 handler lets through with
  `x-amz-metadata-directive: REPLACE`, writes the null version, and its
  source is the version current on the key's OLH. radosgw takes such a copy
  for one onto itself whatever that version is, comparing the key as the
  request named it on both sides
  ([rgw_rados.cc:4922](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4922),
  [rgw_rados.cc:5150](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5150)),
  so while a version with an instance is current the null version names
  that version's tails with no reference of its own and with its tail tag,
  and an overwrite or delete of the null version removes the version's data
  (`docs/ceph-upstream-bugs.md`, "[radosgw's copy onto its own key in a
  suspended bucket shares the current version's tails without
  references](ceph-upstream-bugs.md#radosgws-copy-onto-its-own-key-in-a-suspended-bucket-shares-the-current-versions-tails-without-references)").
  rgw-go takes a copy for one onto itself only when the version the
  source's OLH was followed to is its destination, the null version onto
  itself among them; this copy takes a reference under its tag on each of
  the version's tails and writes its own tail tag, as a copy of another key
  does (`shareTails`, `internal/driver/copy.go`). The two gateways'
  objects read alike; rgw-go's write adds the references and replaces the
  overwritten null version's own tails through the GC, which radosgw's
  `keep_tail` leaks. The S3 ops refuse a versioned write until versioning
  is served.
- **A copy carries none of its source's OLH attrs.** radosgw's copy carries
  every attr of its source's head but `olh.idtag`, `olh.info` and
  `olh.ver`, which it drops only for a bucket whose versioning is not
  enabled
  ([rgw_rados.cc:4809-4813](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4809-L4813),
  [rgw_rados.cc:5022-5026](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5022-L5026)).
  So a source whose OLH head has a modification in flight hands its
  pending attr to the destination, where no log entry will ever remove it
  (`docs/ceph-upstream-bugs.md`, "[radosgw's copy carries the source's
  pending OLH modifications to its
  destination](ceph-upstream-bugs.md#radosgws-copy-carries-the-sources-pending-olh-modifications-to-its-destination)"),
  and a copy of a key's null version, whose head is that key's OLH head,
  into a bucket whose versioning is enabled writes the OLH head's three
  attrs onto the head of the version it creates, which `RGWCopyObj` gives
  every such copy
  ([rgw_op.cc:5576-5580](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5576-L5580),
  [rgw_op.cc:6139-6143](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6139-L6143)),
  and which that version's delete then leaves behind
  (`docs/ceph-upstream-bugs.md`, "[radosgw's copy of a null version leaves
  OLH attrs on the version it writes, and its delete keeps
  them](ceph-upstream-bugs.md#radosgws-copy-of-a-null-version-leaves-olh-attrs-on-the-version-it-writes-and-its-delete-keeps-them)").
  rgw-go drops every `user.rgw.olh.` attr of the source from every copy,
  wherever it goes (`copyAttrs`, `internal/driver/copy.go`). The OLH attrs
  a destination's head carries are its own key's, which its head write
  keeps when it replaces that key's OLH head, and a version rgw-go copies
  from a key's null version leaves nothing once it is deleted.
- **A version's head is removed whole.** radosgw's removal of any head,
  `remove_rgw_head_obj`, keeps the head's `user.rgw.olh.` attrs, which
  `obj_remove` writes back onto an object it creates again
  ([rgw_rados.cc:5798-5803](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5798-L5803),
  [rgw_rados.cc:6410-6415](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6410-L6415);
  [cls_rgw.cc:2410-2482](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2410-L2482),
  [cls_rgw.cc:2578-2650](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2578-L2650)),
  so a version whose head a radosgw copy left OLH attrs on stays, with them
  alone, after its delete (`docs/ceph-upstream-bugs.md`, "[radosgw's copy
  of a null version leaves OLH attrs on the version it writes, and its
  delete keeps
  them](ceph-upstream-bugs.md#radosgws-copy-of-a-null-version-leaves-olh-attrs-on-the-version-it-writes-and-its-delete-keeps-them)").
  rgw-go keeps the OLH attrs only when it removes or resets the head of a
  key without an instance, or of its null version, which is the key's OLH
  head; a version's own head is removed whole, and so is the one a write to
  the same version replaces, and a version's head is never taken for an
  OLH head, whatever attrs it carries (`headKeptAttrs`,
  `internal/driver/delete.go`; `isOLHHead`,
  `internal/driver/put_versioned.go`). A version radosgw's copy made is
  therefore removed whole when rgw-go deletes it. A version's head holds no
  OLH that the attrs could serve: `get_obj_state_impl` follows an OLH only
  for a key without an instance
  ([rgw_rados.cc:6269](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6269),
  [rgw_rados.cc:6883](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6883)).
- **A copy is labeled with its destination's storage class on both
  releases.** rgw-go drops the source's `user.rgw.storage_class` from the
  attrs a copy carries over, on both releases, as Tentacle's radosgw does
  (`driver/rados/rgw_rados.cc:5028` at v20.2.4), so the copy's head write
  labels it from its destination placement alone. Squid's radosgw keeps
  the attr (`:4765-4787` at v19.2.6) and replaces it only with a
  non-empty class (`:3258-3262`), so a copy into a placement's default
  class keeps its source's label, such as COLD, on data in the STANDARD
  pool, and GetObject and HeadObject report it
  (`docs/ceph-upstream-bugs.md`, "[Squid labels a copy to another storage
  class with its source's
  class](ceph-upstream-bugs.md#squid-labels-a-copy-to-another-storage-class-with-its-sources-class)").
  On a Squid cluster rgw-go reports STANDARD for such a copy where
  radosgw reports the source's class.
- **Body tails share one byte budget.** radosgw reads a PUT's body chunk
  by chunk (`get_data`, `rgw_op.cc:4391` at v19.2.6, `:4623` at v20.2.4)
  and bounds only each writer's writes in flight, by
  `rgw_put_obj_min_window_size` (`RadosStore::get_atomic_writer` and
  `RadosMultipartUpload::get_writer`,
  `driver/rados/rgw_sal_rados.cc:2284` and `:3730` at v19.2.6, `:2770` and
  `:4593` at v20.2.4), so the body bytes it holds grow with the PUTs it
  serves. rgw-go also bounds the tail pieces of every PUT, part upload and
  streamed copy at once by one byte budget: its in-flight limiter's byte
  bound, fifteen sixteenths of `objecter_inflight_op_bytes`, raised to one
  piece for each request `rgw_max_concurrent_requests` admits, 4 GiB at
  the defaults. There is no bound without a cap, nor with
  `objecter_inflight_op_bytes` at 0, which turns librados's throttle and
  the limiter off. A piece takes its share before
  it is read and gives it back when its write ends, and a piece larger
  than the whole budget takes all of it. So under load a PUT can wait for
  the budget, with fewer pieces in flight, where radosgw would allocate. A
  client that sends its body slowly holds its piece's share as long as it
  does, since `request_timeout_ms` bounds each read and not the piece, as
  beast's timer does (`StreamIO::recv_body`, `rgw_asio_frontend.cc:152-175`
  at both tags); but the budget keeps a piece for every other request the
  cap admits, so slow clients slow other PUTs and cannot stop them, as
  radosgw's, which read a chunk before its throttle, stop others only by
  filling that cap. The head a PUT keeps for its head write, at most
  `rgw_max_chunk_size`, is outside the budget. A piece is sized from
  `rgw_max_chunk_size` and `rgw_obj_stripe_size` as configured, not as a
  pool's alignment raises them. Only an erasure-coded pool without
  `allow_ec_overwrites` has an alignment, its stripe width
  (`pg_pool_t::requires_aligned_append`, `osd_types.h:1754-1757` at
  v19.2.6, `:1791-1794` at v20.2.4; `RGWRados::get_required_alignment`,
  `driver/rados/rgw_rados.cc:659-693` and `:694-728`). On such a pool with
  a stripe width over 4 MiB a slow client holds more than a piece's room
  and slow clients can stop other PUTs, and a head, one aligned chunk,
  exceeds `rgw_max_chunk_size`. A compressing write's two block buffers, one
  block of plaintext and its encoded form, are outside the budget too, held
  until the body is read.
- **A multipart upload's combined CRC follows its checksum type.** rgw-go's
  completion stores the combination `cksum.NewCombiner` makes
  (`internal/cksum/combine.go`, called from `internal/driver/mp_cksum.go`;
  "CompleteMultipartUpload combines the parts it completes and compares the
  whole supplied checksum"). It combines a multipart upload's part
  checksums as radosgw's completion does
  ([rgw_cksum.cc:96-155](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.cc#L96-L155)
  at v20.2.4): a CRC32, CRC32C or CRC64NVME upload gets the full-object CRC
  combined from its parts' CRCs and sizes, and any other type the type's
  digest over its parts' digests, a composite. It differs in two ways.
  Neither arises on a Squid cluster, whose radosgw ignores
  CreateMultipartUpload's algorithm, and both write records a Tentacle
  radosgw decodes and renders consistently:
  - Every full-object CRC is stored with flags V2, COMBINED and
    FULL_OBJECT, a one-part upload's included, and a Tentacle radosgw
    renders it as the bare CRC with FULL_OBJECT. radosgw keeps a single
    part's own flags, which lack V2, so it reports a one-part CRC upload as
    COMPOSITE: the completion's response sends the bare CRC with ChecksumType
    COMPOSITE, and GET, HEAD and GetObjectAttributes send it as `<crc>-1`
    (`docs/ceph-upstream-bugs.md`,
    "[radosgw reports a single-part CRC upload's full-object checksum as
    COMPOSITE](ceph-upstream-bugs.md#radosgw-reports-a-single-part-crc-uploads-full-object-checksum-as-composite)").
  - A CRC32 or CRC32C upload created with `x-amz-checksum-type: COMPOSITE`
    gets the composite it asked for: the same CRC over its parts' CRCs,
    big-endian and concatenated, stored with flags V2, COMBINED and
    COMPOSITE, which a Tentacle radosgw renders as `<crc>-<parts>` with
    COMPOSITE. radosgw ignores the type: it stores the full-object CRC as
    FULL_OBJECT, and for one part the part's own CRC as the composite
    (`docs/ceph-upstream-bugs.md`, "[radosgw stores a COMPOSITE CRC upload's
    checksum as a full-object
    CRC](ceph-upstream-bugs.md#radosgw-stores-a-composite-crc-uploads-checksum-as-a-full-object-crc)").
- **apply_olh_log's olh.ver guard is EQ on the bytes read, retried from a
  fresh read.** radosgw replays a versioned key's pending log onto its OLH
  head in one op guarded by `cmpxattr(olh.ver, GTE, last epoch)` in the
  OSD's u64 mode, which passes whenever the log's last epoch is at or above
  the stored `olh.ver`
  ([rgw_rados.cc:8434-8435](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8434-L8435),
  [rgw_rados.cc:9180-9181](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9180-L9181)).
  librados' C API compares in string mode only, where a GTE puts "9" above
  "10" (docs/cgo-limitations.md, "librados' C API has no u64-mode
  cmpxattr"). rgw-go guards the op with `cmpxattr(olh.ver, EQ, <the bytes
  it read>)` in string mode (`applyOLHLog`, `internal/driver/olh_head.go`),
  which also refuses an apply when another writer applied any epoch since
  the head was read, where radosgw's lets it through when its log reaches
  that epoch. A refused apply writes nothing and answers ECANCELED, and
  rgw-go then reads the head and the log again and applies anew, up to a
  bound, which keeps the progress radosgw's GTE keeps; past the bound
  ECANCELED is success, as radosgw takes it, and the pending attrs wait for
  the next reader's replay. The attrs written are radosgw's, with two
  differences.
  - The retry compares the log's links with the fresh `olh.ver`. When
    another writer has applied a link at an epoch two links share, the
    retry keeps the version the class made current, where radosgw's pass,
    comparing with the `olh.ver` it read, writes the link logged last
    (`docs/ceph-upstream-bugs.md`, "[radosgw's apply_olh_log applies a
    same-epoch link the class did not make
    current](ceph-upstream-bugs.md#radosgws-apply_olh_log-applies-a-same-epoch-link-the-class-did-not-make-current)").
  - The equality guard also takes a log whose last epoch is below a stored
    `olh.ver` that has not changed since the read, which radosgw's GTE
    refuses. That happens after "[radosgw's clear_olh clears the index's OLH
    entry after a failed head
    removal](ceph-upstream-bugs.md#radosgws-clear_olh-clears-the-indexs-olh-entry-after-a-failed-head-removal)":
    the next link creates the index's OLH entry again under the head's OLH
    tag, the `olh_tag` the link carries ([cls_rgw.cc:1792-1804](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1792-L1804),
    [cls_rgw.cc:1933-1945](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1933-L1945)),
    and `start_modify` starts the new entry at epoch 2
    ([cls_rgw.cc:1503-1517](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1503-L1517),
    [cls_rgw.cc:1630-1644](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1630-L1644)).
    rgw-go then writes `olh.ver` back to that epoch. The link at epoch 2
    lands its `olh.info` only when the tie-break applies it, its instance
    sorting before the head's current one, and the next link lands but in
    one window. A replay that read the head and the log before the epoch-2
    apply, such as a reader's beside the epoch-2 writer's own, can land
    after the next link's apply: its guard finds the `olh.ver` it read,
    written back up again, and its tie-break against the `olh.info` it read
    can write the epoch-2 link over the next one, so that `olh.info` names
    an older version than the index's current one until the key's next
    link. radosgw's replay writes nothing to the head while the log's last
    epoch is below the old `olh.ver`, and its GET answers 404 meanwhile.
    While the new writer's pending attr remains, the follow's `update_olh`
    is refused and the follow answers ENOENT
    ([rgw_rados.cc:8987-8996](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8987-L8996),
    [rgw_rados.cc:9733-9742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9733-L9742)).
    Afterwards `olh.info` still names the version whose unlink led to the
    clear, which has no head: the unlink logged its `REMOVE_INSTANCE`
    ([cls_rgw.cc:1970-1971](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1971),
    [cls_rgw.cc:2117-2118](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2118)),
    the replay's `delete_obj` removed it
    ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541),
    [rgw_rados.cc:9275-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9287)),
    and a delete marker never had one, so the read finds no
    `astate->exists` and answers ENOENT
    ([rgw_rados.cc:7056-7058](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7056-L7058),
    [rgw_rados.cc:7718-7720](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7718-L7720)).
    At the old `olh.ver` radosgw's tie-break decides, its epoch fixed at
    the old one
    ([rgw_rados.cc:8484-8485](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8484-L8485),
    [rgw_rados.cc:9230-9231](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9230-L9231)):
    a link there lands only when its instance sorts before the stale
    `olh.info`'s, and the restarted log's epoch-2 link can land over the
    class's current one, the state rgw-go's window leaves, reached without
    concurrency. Outside that window rgw-go's outcome serves the key
    better, and inside it no worse than radosgw's.

  A read of a versioned key runs this replay, retry included, when it
  meets a pending modification; the versioned writes, which run it too,
  are not served yet.
- **A replay passes over a version removal that the writer's expected write
  tag refuses.** radosgw's `apply_olh_log` removes each version its pending
  log names before its head op, and stops at a removal that fails with
  anything but ENOENT, writing nothing to the head
  ([rgw_rados.cc:8537-8540](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8537-L8540),
  [rgw_rados.cc:9283-9286](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9283-L9286)).
  rgw-go stops the same way, except for a removal it guards on the write
  tag that the replaying writer's own `Expect` read: when that guard refuses
  the removal, the replay passes over the version, lands its head op and its
  trim or clear, and then reports the refusal (`applyOLHLog` and the
  `olhIndex` seam, `internal/driver/olh_head.go`). The version's head and
  instance entry stay; its pending attr goes with the head op and the entry
  leaves the log with the trim or clear. When another writer's pending attr
  refuses the clear, rgw-go trims the log to the replay's last epoch, which
  its head op holds; radosgw's replay, with no refusal to keep, leaves the
  log after a refused `clear_olh`
  ([rgw_rados.cc:8550-8556](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8550-L8556),
  [rgw_rados.cc:9296-9302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9296-L9302)).
  When the refused removal is the null version's, rgw-go trims the log
  where the replay would clear the key's OLH: `clear_olh` removes the OLH
  head object
  ([rgw_rados.cc:8583-8634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8583-L8634),
  [rgw_rados.cc:9329-9380](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9329-L9380)),
  which holds the null version's data the refusal kept, where radosgw's
  replay has removed that data before its clear. The key's OLH entry then
  stays pending removal with no version listed.
  Only a refusal reported without an error is final. One reported with a
  failed trim, index clear or clear expiry leaves the entry in the log, and
  the writer retries the trim or the clear before it acts on the refusal.
  A trim the class refuses because the OLH entry carries another tag is
  final only when the entry is gone, or its pending log holds none of the
  writer's entries: a new link re-tags an entry whose `pending_removal` is
  set and keeps its pending log
  ([cls_rgw.cc:1768-1777](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1768-L1777),
  [cls_rgw.cc:1909-1918](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1909-L1918)).
  Otherwise the refusal is reported as not final and is not retried, since
  no retry restores the old tag, and the new writer's replay of that log
  removes the version as radosgw's does.
  Once the writer's own replay has landed with its trim or clear, no later
  replay meets the entry. Until then a replay can still remove the version
  as radosgw's does, guarded only on the write tag it reads: one by another
  reader running alongside the writer's own, one after the writer's head op
  failed and its retry never landed, every later replay after a trim that
  kept failing, and the replay of a re-tagged entry's kept log. That is the window the
  versioned delete with `Expect` keeps as radosgw's own, where a replay by
  another reader, which does not know `Expect`, removes what it reads.
  radosgw has no `Expect`, so a radosgw writer never meets this case.
  Versioning is not served yet.
- **A replay keeps the null data a later null link names.** The null
  version's removal is logged for the key with an empty instance, not for
  the data it replaced: by a null delete marker's link
  ([cls_rgw.cc:1712-1731](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731) and
  [cls_rgw.cc:1806-1810](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810),
  [cls_rgw.cc:1853-1872](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872) and
  [cls_rgw.cc:1947-1951](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951))
  and by the unlink of `versionId=null`
  ([cls_rgw.cc:1970-1971](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1970-L1971),
  [cls_rgw.cc:2117-2118](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2117-L2118)).
  radosgw's replay removes whatever null data the key's head holds when it
  runs
  ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541),
  [rgw_rados.cc:9275-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9287)),
  so a null PutObject that wrote its head before that link or unlink, and
  linked after it before a replay ran, is listed current with its data and
  its instance entry gone (`docs/ceph-upstream-bugs.md`, "[radosgw's null
  version removal takes the data of a null PutObject that links after
  it](ceph-upstream-bugs.md#radosgws-null-version-removal-takes-the-data-of-a-null-putobject-that-links-after-it)").
  rgw-go's replay drops a removal of the null version that a later link of
  null data in the same replay follows, on the removal's page of the log or
  a later one (`applyOLHLog`, `internal/driver/olh_head.go`;
  `nullLinkAfter`, `internal/driver/olh.go`): that write's head write put
  its own data in the head the two share, and its link lists it current, so
  the state left is the one a null PutObject made after the delete leaves,
  which radosgw reads. Every rgw-go replay does this, a writer's own, a
  reader's and a conditional writer's follow alike; a log without such a
  later link is replayed as before. Two cases still lose the write's data. On a mixed
  fleet, radosgw's replay on a radosgw gateway still removes it whenever it
  is the replay that applies the log. And a replay that runs after the null
  write's head write but before its link is in the log removes the data in
  rgw-go too, after which that write's link fails, as through radosgw. The
  S3 ops that reach it still answer 501 on a versioned bucket.
- **rgw-go removes the instance a failed versioned write made.** A
  versioned write writes its version's head and completes the version's
  instance entry in the bucket index before it links the version under the
  key's OLH. When the link fails, radosgw's `set_olh` cancels its pending
  modification and returns the error
  ([rgw_rados.cc:8715-8737](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8715-L8737),
  [rgw_rados.cc:9461-9483](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9461-L9483)),
  leaving the head and an instance entry that no listing shows, which
  `radosgw-admin bucket check` reports. rgw-go removes them, as its
  unversioned delete removes an object, with the head's removal guarded on
  its write tag (`removeFailedVersion`, `removeUnlinkedVersion`,
  `internal/driver/olh.go`), when the write gives up on an answer that
  proves no link of it landed: the class's or the OSD's, the reshard
  guard's, the write's condition, or every one of radosgw's 100 attempts
  lost to another writer; the awaited completion before the link fails it
  the same way (exclusion "A versioned write awaits its index completion").
  So does any failure of an attempt before its init, in its read of the OLH
  head or in what it checks before the init, a timeout and a context that
  ended included: every earlier attempt ended before its link or on the
  class's refusal of it, so no link of the write is outstanding, and the
  removal runs on past a client's context that ended. A timeout, a context
  that ended or a closed connection at the init or after it leaves the
  outcome unknown, and a version removed after a link that did land would
  be listed with no head, so then rgw-go removes nothing, as radosgw. It
  removes no delete marker, which has no head, not the null version, whose
  head is the key's OLH head, and not the version a CompleteMultipartUpload
  retry links for an earlier completion, whose head names the upload's
  parts ("A CompleteMultipartUpload records itself on the meta object
  before its head write"). A removal that
  fails, such as one a resharding shard still refuses past the reshard
  guard's waits, leaves the version as radosgw leaves it, logged. A failed
  versioned delete makes no version and removes none. Versioning is not
  served yet.
- **A versioned write retries when the key's OLH head goes between its read
  and its init.** radosgw's `set_olh` reads the key's OLH head and then
  inits a pending modification on it, which asserts that a head it read
  still exists; it retries the init's ECANCELED alone and returns its
  ENOENT
  ([rgw_rados.cc:8689-8706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8689-L8706),
  [rgw_rados.cc:9435-9452](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9435-L9452)).
  The head goes when the deletion of the key's last version clears the
  OLH, so a versioned write racing that deletion fails with 404 NoSuchKey
  after its version is written (`docs/ceph-upstream-bugs.md`,
  "[radosgw's versioned write fails when the key's OLH head goes between
  its read and its
  init](ceph-upstream-bugs.md#radosgws-versioned-write-fails-when-the-keys-olh-head-goes-between-its-read-and-its-init)").
  rgw-go's `setOLH` (`internal/driver/olh.go`) takes that ENOENT, on a head
  its read found, as it takes ECANCELED: its next attempt, within radosgw's
  100, reads the head again, finds none, creates it and links, and the
  write succeeds with its version current, which is the state a write
  that came after the deletion leaves. A versioned delete answers the same
  ENOENT NoSuchKey, as radosgw's does: the key it names has no versions
  left. Versioning is not served yet.
- **A versioned link or unlink that logs nothing removes its own pending
  attr.** radosgw's `set_olh` records a pending modification on the key's
  OLH head before its link, and only the replay of the link's log entry
  removes it. A link naming an epoch below the OLH entry's writes the
  version non-current and logs nothing
  ([cls_rgw.cc:1751-1760](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1751-L1760),
  [cls_rgw.cc:1892-1901](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1892-L1901)),
  and `apply_olh_log` returns at once on an empty log
  ([rgw_rados.cc:8423-8425](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8423-L8425),
  [rgw_rados.cc:9169-9171](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9169-L9171)),
  so the attr stays until `rgw_olh_pending_timeout_sec`, an hour by
  default, expires it. Until then every read of the key runs `update_olh`
  from `follow_olh`
  ([rgw_rados.cc:8984-8996](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8984-L8996),
  [rgw_rados.cc:9730-9742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9730-L9742)).
  A lifecycle transition of a noncurrent version sends such a link, naming
  the version's own epoch
  ([rgw_lc.cc:1515-1516](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1515-L1516),
  [rgw_lc.cc:1561-1562](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1561-L1562)).
  An unlink naming an epoch comes from a system request's
  `rgwx-versioned-epoch`
  ([rgw_op.cc:5268-5285](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5268-L5285),
  [rgw_op.cc:5657-5674](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5657-L5674))
  or from data sync's `RGWAsyncRemoveObj`
  ([rgw_cr_rados.cc:946](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_cr_rados.cc#L946),
  [rgw_cr_rados.cc:1001](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_cr_rados.cc#L1001)),
  and only radosgw's own multisite connection sends `rgwx-` parameters.
  rgw-go removes its own pending attr after its replay, as a failed link's
  cancel removes it, when the replay, reading the log from its start, read
  every page and found no entry under the modification's op tag
  (`finishOLHModification`, `internal/driver/olh.go`); an unlink that logs
  nothing, of a delete marker at an older epoch, does the same. An entry
  another writer applied and trimmed had its attr removed by that apply, so
  the removal finds nothing, and an entry still in the log keeps its attr,
  for the next reader's replay. With `skip_olh_obj_update` there is no
  replay and the attr stays, as radosgw leaves it. Versioning is not served
  yet.
- **A versioned link or unlink whose outcome is unknown keeps its pending
  attr.** radosgw's `set_olh` cancels its pending modification on every
  failed link, a timeout included
  ([rgw_rados.cc:8715-8717](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8715-L8717),
  [rgw_rados.cc:9461-9463](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9461-L9463)),
  and `unlink_obj_instance` on every failed unlink
  ([rgw_rados.cc:8823-8824](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8823-L8824),
  [rgw_rados.cc:9569-9570](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9569-L9570)),
  though one that timed out may still land, and then no read of the key
  replays it (`docs/ceph-upstream-bugs.md`, "[radosgw's set_olh cancels the
  pending attr of a link that timed out, though it may have
  landed](ceph-upstream-bugs.md#radosgws-set_olh-cancels-the-pending-attr-of-a-link-that-timed-out-though-it-may-have-landed)").
  rgw-go keeps the attr when the failure leaves the outcome unknown, a
  timeout, a context that ended or a closed connection (`outcomeUnknown`,
  `internal/driver/olh.go`), and cancels it on any other failure, as
  radosgw does. The best-effort update after the failure, which radosgw
  runs too, applies a link or unlink that has landed by its read of the
  log; one that lands later is applied by the next read of the key, whose
  follow replays the log while the attr remains
  ([rgw_rados.cc:8984-8996](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8984-L8996),
  [rgw_rados.cc:9730-9742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9730-L9742)),
  or by the key's next writer. A timed-out unlink that lands thus has its
  version removed by rgw-go's next read of the key, where radosgw's GET
  keeps serving the unlinked version until the key next changes, as it
  keeps serving the previous version after a timed-out link that lands.
  A link or unlink that never landed leaves the attr until a follow
  expires it past `rgw_olh_pending_timeout_sec`, an hour by
  default, as `check_pending_olh_entries` expires any pending attr
  ([rgw_rados.cc:8904-8934](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934),
  [rgw_rados.cc:9650-9680](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680));
  until then each read of the key runs a replay that finds nothing to
  apply, and the attr refuses the clear of the key's OLH, as any writer's
  pending attr does. The versioned writes are not served yet.
- **A versioned delete of versionId=null checks the null version itself.**
  A delete naming a version of a versioned key reads that version's index
  entry, checks If-Match, `x-amz-if-match-size` and
  `x-amz-if-match-last-modified-time` against the entry's meta, and reports
  the entry's delete-marker flag
  ([rgw_rados.cc:5905-5953](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5905-L5953),
  [rgw_rados.cc:6517-6565](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6517-L6565)).
  radosgw reads it with one `bi_get` of the key as named, `null` cleared
  (`bi_get_instance`,
  [rgw_rados.cc:9443-9463](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9443-L9463),
  [rgw_rados.cc:10192-10212](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10192-L10212)),
  which for an empty instance reads the plain key, as
  `encode_obj_index_key` keys it
  ([cls_rgw.cc:339-346](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L339-L346),
  [cls_rgw.cc:389-396](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L389-L396)).
  From a key's first link its plain key holds the class's version marker, an
  entry with empty meta that is no delete marker, and the null version's
  entries, its data and its delete marker, sit at keys of their own
  ([cls_rgw.cc:1568-1634](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1568-L1634),
  [cls_rgw.cc:1695-1763](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1695-L1763)).
  So radosgw compares a `versionId=null` delete's conditions with the
  marker's empty meta, answering 412 for any If-Match naming an ETag, any
  `x-amz-if-match-size` but 0 and any `x-amz-if-match-last-modified-time`,
  and omits `x-amz-delete-marker`, which it sends only when true
  ([rgw_rest_s3.cc:3496-3497](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3496-L3497),
  [rgw_rest_s3.cc:3746-3747](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3746-L3747)),
  from the removal of a null delete marker
  (`docs/ceph-upstream-bugs.md`, "[radosgw's versioned delete of
  versionId=null checks the version marker, not the null
  version](ceph-upstream-bugs.md#radosgws-versioned-delete-of-versionidnull-checks-the-version-marker-not-the-null-version)").
  rgw-go resolves `null` in the class's `read_key_entry` order, the plain
  key, then the null delete marker's key, then the null data key
  (`getInstanceEntry`, `internal/driver/olh_index.go`), and checks the
  conditions and reports the flag of the null version's own entry. The cost
  is up to two more index reads per `versionId=null` delete. On a key with no
  null version rgw-go answers NoSuchKey from those reads, writing nothing,
  where radosgw inits a pending modification on the key's OLH head and
  cancels it before it answers the unlink's ENOENT, or answers 412 under a
  condition. The
  S3 delete still answers 501 on a versioned bucket ("Writes to a versioned
  or object-lock bucket answer 501 until versioning is served"); the
  object store's delete does this.
- **A conditional delete marker is checked against the version current when
  it links.** A delete naming no version on a versioned bucket writes a
  delete marker. radosgw checks its If-Match, `x-amz-if-match-size` and
  `x-amz-if-match-last-modified-time` once, against the version current
  through the key's OLH, and then links the marker over whatever another
  writer linked since
  ([rgw_rados.cc:5882-5901](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5882-L5901),
  [rgw_rados.cc:6494-6513](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6513)).
  rgw-go makes them the marker link's condition (`writeDeleteMarker`,
  `internal/driver/delete_versioned.go`), checked as every conditional
  versioned write's is (`prepareOLHAttempt` and `checkOLHLink`,
  `internal/driver/olh_cond.go`), by the rules exclusion "A conditional
  write to a versioned bucket commits only if its condition held when it
  linked" states for the check at each attempt, the guard on the attempt's
  init and its wait, and the check after the link. On an enabled bucket the
  marker commits only if the conditions held when it linked: the check
  after its link unlinks the marker and answers 412 when another version or
  marker took an epoch between the one the attempt read and the marker's
  own, or when the version the conditions passed on, the current delete
  marker they saw, or the null version whose data they passed on, is no
  longer listed. A pass of any of the three conditions rests on
  what it was checked against, so the current version, or the current
  delete marker and, for a pass on the null version's data in the key's
  head, the null version, must stay listed. On a suspended bucket the
  marker is the null
  version, whose link replaces the null version's list entry and logs the
  removal of its data
  ([cls_rgw.cc:1712-1731](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731),
  [cls_rgw.cc:1853-1872](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872)),
  which no unlink restores. So it is not checked after its link: its init's
  guard serializes it against rgw-go's conditional writers alone, and a
  radosgw writer, or an unconditional rgw-go one, that links between its
  init and its link stays under the marker, as through radosgw. For the
  same reason no link of the null version is checked after it; what the
  null version's data adds, its head write carrying its init before its
  link, is in exclusion "A conditional write to a versioned bucket commits
  only if its condition held when it linked".

  When no version is current, a delete marker being current or no link
  having reached the key's head, the conditions are checked against the
  key's head without following it, the OLH head object, as radosgw's are
  ([rgw_rados.cc:5882-5891](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5882-L5891),
  [rgw_rados.cc:6494-6503](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6494-L6503)):
  If-Match: `*` passes and writes another marker, and an If-Match naming an
  ETag answers 412 unless the head holds a noncurrent null version's data
  under that ETag, which s3-tests' `test_delete_object_current_if_match`
  expects. radosgw checks the head as it read it before its follow, and
  falls back to it when that follow's replay is refused, where rgw-go reads
  the head again after the replay. A pass on the head's ETag, size or
  last-modified time while the head holds the null version's data rests on
  that version, so on an enabled bucket the check after the link requires
  it listed too, reading the key's listing down to its entry; the null
  version's removal leaves the head with no ETag and no data, which
  If-Match: `*` alone does not compare. The S3 delete still answers 501 on
  a versioned bucket; the object store's delete does this.
- **A null delete marker can be conditional on the null version it
  replaces.** On a bucket whose versioning is suspended, a delete naming no
  version links the null delete marker, whose link takes the null version's
  list entry and logs the removal of its data, wherever that version sits
  ([cls_rgw.cc:1712-1731](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1731),
  [cls_rgw.cc:1806-1810](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1806-L1810);
  [cls_rgw.cc:1853-1872](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1872),
  [cls_rgw.cc:1947-1951](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1947-L1951)),
  and the replay that follows removes whatever null data the key's OLH head
  holds when it runs
  ([rgw_rados.cc:8530-8541](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8541),
  [rgw_rados.cc:9275-9287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9287)),
  a null PutObject's data included when that PUT links after the marker
  (`docs/ceph-upstream-bugs.md`, "[radosgw's null version removal takes the
  data of a null PutObject that links after
  it](ceph-upstream-bugs.md#radosgws-null-version-removal-takes-the-data-of-a-null-putobject-that-links-after-it)").
  radosgw's delete takes no expected state. rgw-go's takes the null version
  as its caller read it, `DeleteParams.Expect`, which the lifecycle and the
  object-lock checks are to pass: the marker's link carries its
  `user.rgw.idtag`, absent read as empty (`writeDeleteMarker`,
  `internal/driver/delete_versioned.go`). Each attempt compares it on the
  OLH head it reads and the attempt's init compares it again, so a null
  write or an attr write that landed before the init answers 409
  ConcurrentModification with no marker linked, and the delete's own
  replay removes the null data only while the head carries it. A removal
  that replay refuses is answered as the exclusion "A refused versioned
  removal re-links the version, unless a newer null write owns the null
  key" says. The delete runs its own replay even when told to skip it,
  since the guard is in that replay. On a bucket whose versioning is
  enabled the marker removes nothing and `Expect` is not read. The
  windows that exclusion names stay. The S3 delete still answers 501 on a
  versioned bucket; the object store's delete does this.
- **A refused versioned removal re-links the version, unless a newer null
  write owns the null key.** A versioned delete removes a version in two
  steps: the class's unlink, or the null delete marker's link, takes its
  list entry and logs its removal
  ([cls_rgw.cc:1867-2018](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1867-L2018),
  [cls_rgw.cc:2002-2159](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2002-L2159)),
  and the replay removes its head and its instance entry. radosgw never
  refuses that removal. rgw-go's delete with `DeleteParams.Expect` checks
  the version before the unlink, answering 409 ConcurrentModification with
  nothing written when it no longer matches, and its own replay, which it
  runs even when told to skip it, removes the head only under the write tag
  `Expect` read, and removes no data for a delete marker.
  - A version with a head must still carry that `user.rgw.idtag`. For the
    null version, whose head is the OLH head, the unlink's init compares the
    tag too, as an init gives a head without an OLH tag a fresh one, and the
    replay's removal is guarded on that fresh tag.
  - A delete marker has no head, and every marker's index entry carries the
    tag `delete-marker`
    ([cls_rgw.cc:1350-1358](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1350-L1358),
    [cls_rgw.cc:1476-1484](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484)),
    so `Expect` must be a marker with the entry's `versioned_epoch` and
    mtime, which a marker's state read by its version carries. A second null
    delete marker or a null write that replaced the null marker before the
    delete read its entry answers 409 with nothing written. The class's
    unlink of a marker compares no entry, so the null marker's unlink also
    compares, in the same RADOS op as the class's call, the marker's
    instance entry, `\x801000_<name>\0i\0d`
    ([cls_rgw.cc:325-337](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L325-L337),
    [cls_rgw.cc:375-387](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L375-L387)),
    with the raw value the delete's read returned
    ([PrimaryLogPG.cc:7802-7874](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L7802-L7874),
    [PrimaryLogPG.cc:7879-7951](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L7879-L7951)).
    A second null marker rewrites that entry and a null write's link removes
    it, so either, landing after the read and before the unlink, refuses the
    unlink with nothing unlinked; the delete reads the marker again and
    answers 409. Two rewrites keep the marker and change its bytes: a
    version linked over it clears its current flag
    ([cls_rgw.cc:1429-1431](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1429-L1431),
    [cls_rgw.cc:1556-1558](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1556-L1558)),
    and the unlink of the version above it sets it again
    ([cls_rgw.cc:1948](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1948),
    [cls_rgw.cc:2095](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2095)).
    After either, or a reshard's copy, the read again finds the same
    `versioned_epoch` and mtime, and the unlink is tried again with the
    bytes read now. A marker owns no data, so its unlink compares no head,
    and its replay guards any removal on a write tag no head carries.

  When the head is rewritten after the unlink or the marker's link, by a
  retention, an ACL or a new null write, the replay refuses the removal,
  and the version would stay readable by its id but absent from
  ListObjectVersions and from the lifecycle's listings. rgw-go lists it
  again and answers 409 ConcurrentModification (`refusedRemoval`,
  `relistVersion` and `relistNull`, `internal/driver/delete_versioned.go`).
  - Only a final refusal is acted on. One returned with a trim, an index
    clear or a clear expiry that failed, or with the writer's entries kept
    in the log of an OLH entry another writer re-tagged, leaves the removal
    in the log, where a later replay removes the version, so rgw-go lists
    nothing and answers that error.
  - Nothing is listed when the version's instance entry or its head is
    gone, another replay having removed it.
  - A named version that no listed version is newer than is linked at
    epoch 0, current again at a fresh epoch, the version the unlink
    promoted demoted. It is linked in up to five rounds, each reading the
    version, the OLH head and the listing afresh; before it reads the head, a
    round removes the pending attrs past `rgw_olh_pending_timeout_sec`, as
    radosgw's `check_pending_olh_entries` and rgw-go's follow do
    ([rgw_rados.cc:8904-8934](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8904-L8934),
    [rgw_rados.cc:9650-9680](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9650-L9680)).
    A round's link is guarded
    on the OLH head as that round read it: each attempt's read must still
    carry its OLH tag, its `olh.info`, its `olh.ver` and no pending attr,
    and the init compares `olh.info`, `olh.ver` and the pending attrs again,
    so a modification applied since, one that left `olh.info` as it was
    included, refuses it, and the next round reads again. While another
    writer's modification is pending, a round first waits for it as
    rgw-go's conditional writers do, 318 to 635 ms in all, with no wait on a
    pending attr already older than 318 ms when first read (exclusion "A
    conditional write to a versioned bucket commits only if its condition
    held when it linked"). Past the wait, pending attrs no longer refuse the
    link, which is then guarded on `olh.info` and `olh.ver` alone. So a
    pending attr no writer will remove, such as the one radosgw's link at an
    old epoch leaves, does not hold the version off: before
    `rgw_olh_pending_timeout_sec` expires it the wait passes it, and after,
    the round removes it.
  - When a newer version is listed, or every round lost its link to another
    writer, the version is linked at its own epoch, non-current, after its
    instance record's flags are reset to `FLAG_VER` alone: the unlink left
    the record as it was, flagged current when it was current, and the
    class's link at an old epoch adds `FLAG_VER` and clears nothing
    ([cls_rgw.cc:1410-1427](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1410-L1427),
    [cls_rgw.cc:1537-1554](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1537-L1554)),
    so it would list a second entry of the name flagged current. The reset
    is a prepare and an awaited cancel carrying the record's own version: the
    class's completion resets the flags whatever it completes, and its cancel
    removes the placeholder the prepare makes when the record went since it
    was read
    ([cls_rgw.cc:851-856](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L851-L856) and
    [cls_rgw.cc:1061-1126](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1061-L1126),
    [cls_rgw.cc:983-988](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L983-L988) and
    [cls_rgw.cc:1196-1260](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1196-L1260)).
    With a newer version listed the version goes behind it. With none, when
    each of the five rounds lost its link to another writer's modification
    that lists nothing newer, such as unlinks of other versions, the version
    lists non-current ahead of the version the unlink promoted, which stays
    current, and ListObjectVersions marks the name's second entry IsLatest.
    The class promotes only a version that lists after the current one
    ([cls_rgw.cc:1437-1473](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1437-L1473),
    [cls_rgw.cc:1564-1600](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1564-L1600)),
    so it never promotes the version left ahead. The current version's next
    unlink promotes the next older one, or, with none listed, clears the
    key's OLH, its OLH entry and its plain entry
    ([cls_rgw.cc:2126-2190](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2126-L2190),
    [cls_rgw.cc:2279-2355](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2279-L2355)),
    while the version stays listed with no version current, ahead of any
    version a later write links on the OLH made again, whose epochs start
    again at 2. Short of such a clear, the key's next link lists its version
    above it and ends the misordering.
  - When the key's OLH entry carries another tag than the one the unlink
    sent, the entry cleared and made again since, whose epochs start again
    at 2, the version is linked at epoch 1, below every version the new
    entry holds.
  - The null version's data has one record for every null write, so each
    round reads it again and lists nothing when its `versioned_epoch` is no
    longer the one the first round read, a newer null write owning the key,
    when the OLH head no longer holds its data, or when it is listed. The
    class's link of the null data takes the null delete marker's list and
    instance entries whatever its epoch, and a link at an old epoch writes no
    OLH entry
    ([cls_rgw.cc:1712-1760](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1760),
    [cls_rgw.cc:1853-1901](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1901)),
    so while the null delete marker is current the null data is linked at
    epoch 0. When the version the marker demoted is newer than the null
    data, that version is linked at epoch 0 first, current again, and the
    null data then goes below it. Otherwise the null data goes at its own
    epoch, its record reset first, as a named version's fallback does.
    Unlike that fallback, each of these links is guarded on the OLH head the
    round read, as the named version's epoch-0 link is, after the same wait
    on pending modifications; a round whose link that guard refuses gives
    way to the next, up to five, after which the version stays unlisted.
  - A refused removal of the null version that leaves the key no listed
    version has the replay trim the log where it would clear the key's
    OLH: the clear removes the OLH head object, which holds the data the
    refusal kept, where radosgw's replay has removed that data first.

  Exactly one entry of the name is then flagged current, but for two
  windows of rgw-go's own, since radosgw never re-links, and one of
  radosgw's:
  - A writer that inits after a guarded link's init and links before it,
    and, once a round's wait has passed or found only stale attrs, a writer
    that links between the round's read and the re-link's link, before its
    replay lands. Against the epoch-0 link the later link wins, by radosgw's
    rule, and that writer's version ends below the restored one. Against
    the null data's own-epoch link, a null write that links in between stays
    current, but the link writes the null record back at the old epoch, and
    with it that write's list entry, below the newer versions and still
    flagged current: `BIVerObjEntry::write`, cited above, moves the record's
    list entry and clears no flag.
  - A replay running alongside the own one, which read the own entries
    before the own tail dropped them, can land its removal at any time after
    the check that the version is still there, before the re-link's link or
    after it. The window lasts until that removal lands, and the list entry
    the re-link wrote stays after it, for a version with no head: the
    removal's index completion takes the instance entry alone
    ([cls_rgw.cc:1127-1167](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1127-L1167),
    [cls_rgw.cc:1261-1300](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1261-L1300)).
  - radosgw's own: a replay by another reader, which does not know
    `Expect`, removes what it reads, as radosgw's does. Until the writer's
    own replay has landed with its trim or clear, a replay by another reader
    running alongside the own one, and one after the own head op failed and
    its retry never landed, removes the version guarded only on the write
    tag it reads.

  The S3 delete still answers 501 on a versioned bucket; the object store's
  delete does this.
- **A conditional write to a versioned bucket commits only if its condition
  held when it linked.** radosgw checks a versioned write's If-Match and
  If-None-Match once, before its head write, and then links the version
  under the key's OLH with nothing tying the link to that check: the init of
  its pending modification is guarded on the OLH tag alone
  ([rgw_rados.cc:3187-3205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3187-L3205),
  [rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375) and
  [rgw_rados.cc:7799-7910](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7799-L7910) at v19.2.7,
  [rgw_rados.cc:3284-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3284-L3300),
  [rgw_rados.cc:3490-3496](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3490-L3496) and
  [rgw_rados.cc:8462-8573](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8462-L8573) at v20.2.4).
  So two `If-None-Match: *` writes can both create the key, and an
  If-Match write can link after the version it matched was replaced
  (`docs/ceph-upstream-bugs.md`, "[radosgw's conditional versioned write
  checks its condition once and links
  unguarded](ceph-upstream-bugs.md#radosgws-conditional-versioned-write-checks-its-condition-once-and-links-unguarded)").
  rgw-go checks the condition at radosgw's point too, and a write refused
  there writes nothing; its link then holds the condition
  (`linkCond`, `internal/driver/put_versioned.go`; `prepareOLHAttempt`,
  `waitOLHPending` and `checkOLHLink`, `internal/driver/olh_cond.go`).
  - Each attempt of the link follows the key's OLH head as a read does,
    replaying a pending modification and reading the head again when the
    replay applied anything or another writer refused a pass of it, and
    checks the condition on the version current on that head, a current
    delete marker counting as no object. That is the version current after
    the replay, where radosgw checks the one current before it and, when its
    replay is refused, none (exclusion "A read that replays a pending
    modification answers the version current after it"). A condition that
    no longer holds answers 412 PreconditionFailed, or 404 NoSuchKey for an
    If-Match with no current version, and removes the version the write
    made through the unversioned delete on its instance key.
  - The attempt's init is guarded on the head's `olh.info` as the attempt
    read it, absent read as empty, and, while no pending attr remains that
    the attempt does not wait on, on there being no pending attr
    (`obj_check_attrs_prefix` of `user.rgw.olh.pending.`, failing if one
    exists). That serializes rgw-go's conditional writers. An attempt that
    meets another writer's pending attr runs the replay and backs off, with
    jitter, each backoff one of radosgw's 100 attempts, up to 8 backoffs,
    318 to 635 ms in all; past them its init no longer refuses a pending
    attr, and the check after its link alone holds the condition. Pending
    attrs older than `rgw_olh_pending_timeout_sec` are expired first, as
    `follow_olh` expires them
    ([rgw_rados.cc:8984-8996](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8984-L8996),
    [rgw_rados.cc:9730-9742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9730-L9742)),
    and one already older than 317.5 ms, the backoffs' least, when the
    attempt first reads it is not waited on. That age compares the clock of
    the gateway that wrote the attr with rgw-go's, so a skew past 317 ms
    takes a live writer's attr for stale and skips the wait, or a dead
    writer's for live and spends the bound; a named version's condition
    does not rest on the wait. A pending attr no writer will remove, such as
    the one radosgw's link at an older epoch leaves, delays the key's
    conditional writes by the bound until it is that old or expires.
  - radosgw's writers and rgw-go's unconditional ones guard their init on
    the OLH tag alone, so they can link between a conditional writer's init
    and its link. After its link lands, and before its replay, the writer of
    a named version lists the key's versions on its shard and voids the link
    when another version or delete marker holds an epoch above the
    `olh.ver` its attempt read and below its own, or when the version or
    delete marker its condition rested on is no longer listed. A condition
    rests on the current version for any comparison with it, If-None-Match
    with an ETag included, and on a current delete marker whatever it
    compares, since the marker's removal makes the version below it current
    again without listing anything above the epoch the attempt read;
    `If-None-Match: *` over no version rests on nothing. An unlink takes no
    epoch: the unlink of the current version promotes the next one at that
    version's own epoch
    ([cls_rgw.cc:1388-1427](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1388-L1427) and
    [cls_rgw.cc:1936-1967](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1936-L1967) at v19.2.7,
    [cls_rgw.cc:1514-1554](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1514-L1554) and
    [cls_rgw.cc:2083-2114](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2083-L2114) at v20.2.4),
    so only the rule on the version the condition rested on sees one. A
    void link is unlinked through the versioned unlink, whose replay removes
    the version, promoting the other writer's when the void one was
    current, and the write answers 412. When another writer removed the
    writer's own version after its link, that version's epoch is unknown:
    the write answers 412, with nothing to unlink, when the version its
    condition rested on is gone or another version or marker holds any
    epoch above the one it read but that version's, and success otherwise.
    A listing that fails leaves the condition unknown, so the link is
    unlinked too and the listing's error answered, a 5xx. An unlink whose
    outcome is unknown answers its error, and one that fails leaves the
    version linked and answers 500 InternalError.
  - Unconditional writes keep radosgw's guards exactly.

  The check after the link is conservative: a write whose condition held
  when it linked answers 412 when the version its condition rested on is
  deleted between its link and its check, or when `If-Match: *` rested on a
  version another writer deleted though an older one survives, and two
  racing conditional writers may both answer 412. A voided version can be
  read and listed between its link and its unlink. The guarantee is not kept
  when the writer stops between its link and its check, nor when its void
  unlink is not sent: the key's next replay commits the link. After a
  restarted log, a failed `clear_olh` head removal followed by radosgw's
  replay (exclusion "apply_olh_log's olh.ver guard is EQ on the bytes read,
  retried from a fresh read"), the head's `olh.ver` can exceed the OLH
  entry's epoch, so another writer's link at a lower epoch escapes the epoch
  rule.

  The null version is covered only up to its head write. No link of the
  null version is checked after it (exclusion "A conditional delete marker
  is checked against the version current when it links"). The null
  version's data is written over the key's OLH head before its link, so
  that link carries no condition, the head then holding the write's own
  data. Its condition is checked before the head write instead, as a
  conditional link's attempt checks it, on the version current on the
  followed head (exclusion "A suspended bucket's conditional write is
  checked against the current version"), and the head write's own op, not
  the link, inits the write's pending modification (`writeNullVersion`,
  `internal/driver/put_versioned.go`). Ahead of every other step the op
  carries a conditional link's init guards, on the head's OLH tag, on its
  `olh.info` as the check read it and, while the wait allows it, on no
  pending attr, and then radosgw's own guard on the write tag it read, none
  for `If-None-Match: *`; after them come the head write, which keeps the
  OLH attrs, and the new pending attr. The OSD runs an op's steps in order
  and applies all of them or none, so the guards see the head as it was
  before the op, and what lands is the init followed by the head write with
  nothing between. The link is then made under the init's op tag, with no
  init of its own. Every step is one radosgw sends on an OLH head; only the
  init's place moves, from the link into the head write's op. A second
  conditional write meets the first's pending attr from its head write to
  its replay, so within the bound it waits, as a conditional link waits,
  and checks its condition again on the version the first's link made
  current: two conditional writes of the null version are serialized, the
  second answering 412 when the first made its condition false, as radosgw
  answers it. An op the init's guards refuse, another writer having applied
  or recorded a modification since the check, writes nothing, and the write
  checks its condition again from a fresh read of the head, answering 412
  when it no longer holds. An op that finds the head gone writes nothing
  too, and the fresh read takes the unversioned path: an If-Match answers
  404 NoSuchKey, and `If-None-Match: *` writes the key as a plain object
  with no link. radosgw answers that race from its lost-race table
  instead, an If-Match ETag with 404 as rgw-go does. For an `if_match` of
  `*` that table turns ENOENT into `ERR_PRECONDITION_FAILED`, 412
  ([rgw_rados.cc:3425-3430](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3425-L3430),
  [rgw_rados.cc:3546-3551](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3546-L3551)).
  One the write tag's
  guard refuses, another write or removal of the null version having
  replaced the head, writes nothing and is answered as radosgw answers a
  lost race. One whose outcome is unknown may have landed, its pending attr
  then left to expire, and a completion that fails cancels the attr.

  What the null write's guards leave unheld:
  - The pending-attr guard holds within the bound alone, and only while no
    pending attr remains that the write does not wait on, a stale one
    included: a dead writer's, radosgw's at an older epoch, or a conditional
    null write's whose head write timed out, each until it expires. Outside
    that the head write is guarded on `olh.info` alone, which changes only
    when a modification is applied, so a modification another writer
    records and links after the check and applies only after the head write
    is linked over. A second conditional write of the null version is such a
    writer: past the bound, or beside a pending attr not waited on, two
    conditional null writes can both answer 200, the null version holding
    the data of the one whose head write came last, where radosgw at both
    floors answers the second 412, its check reading the head the first
    wrote. There the guarantee does not hold.
  - From the head write, which carries the null write's init, to its link,
    a writer that does not wait on the null write's pending attr, which is
    radosgw's writers, rgw-go's unconditional ones and its conditional ones
    past their bound, and that inits after the head write and links before
    the null version's link, is linked over, the null version current after
    it, whatever the condition says and whether or not that writer applies
    its link in between; no guard on the link could undo a head write that
    already replaced the null version's data.

  radosgw has each of these windows, the second null write's aside, and a
  wider one: it checks the condition on the OLH head as read, not on the
  current version, and guards nothing between that check and its link but
  the write tag, none for `If-None-Match: *`, so a writer that links a
  version between its check and its link is linked over however it
  interleaves
  ([rgw_rados.cc:3185-3205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3185-L3205) and
  [rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375) at v19.2.7,
  [rgw_rados.cc:3282-3300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3300) and
  [rgw_rados.cc:3490-3496](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3490-L3496) at v20.2.4).
  A second conditional null write is the one interleaving the floors refuse
  and rgw-go refuses only within its bound: their check reads the head the
  first wrote, which exists for `If-None-Match: *` and holds the first's
  ETag for an If-Match. Main and the tentacle branch after v20.2.4 follow the
  OLH for that check, and let both through (`docs/ceph-upstream-bugs.md`,
  "[radosgw's conditional versioned write checks its condition once and
  links
  unguarded](ceph-upstream-bugs.md#radosgws-conditional-versioned-write-checks-its-condition-once-and-links-unguarded)").

  The state written is radosgw's. Squid needs nothing beyond this: v19.2.7
  checks a versioned write's conditions against the current version as
  v20.2.4 does, since ce98cebb815 (`docs/ceph-upstream-bugs.md`, "[radosgw
  before v19.2.7 and v20.2.1 checked a versioned write's conditions against
  the new
  instance](ceph-upstream-bugs.md#radosgw-before-v1927-and-v2021-checked-a-versioned-writes-conditions-against-the-new-instance)").
  The S3 ops refuse a versioned write until versioning is served.
- **A suspended bucket's conditional write is checked against the current
  version.** radosgw checks a write's If-Match and If-None-Match against the
  version current on the key's OLH only when the key has an instance; a
  write without one, as every write to a bucket whose versioning is
  suspended is, is checked against the key's head as read without following
  the OLH
  ([rgw_rados.cc:3185-3197](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3185-L3197),
  [rgw_rados.cc:3282-3294](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3294)).
  Over a key that was ever versioned that head is the OLH head, which exists
  whatever version is current and holds the null version's data or none:
  radosgw answers `If-None-Match: *` 412 over a current delete marker,
  passes `If-Match: *` there, and compares an ETag with the null version's,
  or with none, whichever version is current (`docs/ceph-upstream-bugs.md`,
  "[radosgw checks a suspended bucket's write conditions against the OLH
  head](ceph-upstream-bugs.md#radosgw-checks-a-suspended-buckets-write-conditions-against-the-olh-head)").
  rgw-go checks a write over an OLH head against the version current on it,
  followed as a read follows it, a current delete marker counting as no
  object, as it checks a write with an instance (`condState`,
  `internal/driver/put_versioned.go`). A head without OLH attrs, a key never
  versioned, is checked as it is on both gateways. The check comes before
  the write's head write, which carries the init of its modification and is
  guarded on what the check read, so two conditional writes of the null
  version are serialized within the bound, the second checked on the
  version the first made current; past the bound, or beside a pending attr
  the write does not wait on, both can answer 200 where radosgw at both
  floors answers the second 412, and nothing holds the condition from the
  head write to the link (exclusion "A conditional write to a versioned
  bucket commits only if its condition held when it linked"). s3-tests'
  `test_put_object_suspended_if_match` expects the answers rgw-go's
  PutObject gives. The S3 ops refuse a versioned write until versioning is
  served.
- **A versioned write whose link fails keeps its tails.** radosgw's PutObject
  deletes the tails it wrote whenever its head's `write_meta` fails other
  than by a timeout
  ([rgw_putobj_processor.cc:393-401](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L393-L401) and
  [rgw_putobj_processor.cc:184-229](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_putobj_processor.cc#L184-L229) at v19.2.7,
  [rgw_putobj_processor.cc:427-436](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L427-L436) and
  [rgw_putobj_processor.cc:212-257](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_putobj_processor.cc#L212-L257) at v20.2.4),
  and a versioned write's `write_meta` returns `set_olh`'s failure after its
  head is written and its index entry completed, whether or not the link
  landed
  ([rgw_rados.cc:3369-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3369-L3375),
  [rgw_rados.cc:3490-3496](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3490-L3496)):
  the version's head, linked or not, then names deleted tails
  (`docs/ceph-upstream-bugs.md`, "[radosgw's versioned PutObject deletes its
  tails when its link
  fails](ceph-upstream-bugs.md#radosgws-versioned-putobject-deletes-its-tails-when-its-link-fails)").
  rgw-go deletes a write's tails only when its head write itself failed.
  Once the head is written its tails stay whatever the completion or the
  link answers, and a removal of the version, after a completion or a link
  refused or a link voided, sends them to the GC as the unversioned delete
  does (`putStream`, `internal/driver/put.go`; exclusion "rgw-go removes the
  instance a failed versioned write made"). The same holds for what the
  other versioned writes leave:
  - a CopyObject that shares its source's tails keeps the references it
    took on them once its head is written, where `copy_obj`'s `done_ret`
    drops them after `set_olh` fails, leaving a version whose tails go with
    their source; the removal of the version queues them under the copy's
    tag, which drops the references (`shareTails`,
    `internal/driver/copy.go`; `docs/ceph-upstream-bugs.md`, "[radosgw's
    versioned copy drops its tail references when its link
    fails](ceph-upstream-bugs.md#radosgws-versioned-copy-drops-its-tail-references-when-its-link-fails)");
  - a CompleteMultipartUpload keeps its completion record once its head is
    written, so its upload is never completed again over the parts that
    head names: a retry links the version the record names when no link of
    it landed, keeping it whatever that link answers, and answers 500
    InternalError for a version it cannot link ("A CompleteMultipartUpload
    records itself on the meta object before its head write").

  The S3 ops refuse a versioned write until versioning is served.
- **A versioned write is counted in the quota cache before its link.**
  radosgw adds a versioned PutObject's object and bytes to the bucket's
  quota cache only once `set_olh` has succeeded, its `update_stats` coming
  after the link
  ([rgw_rados.cc:3390-3398](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3390-L3398),
  [rgw_rados.cc:3511-3519](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3511-L3519)).
  rgw-go adds them once the version's head is written, before its
  completion and its link (`doWriteMeta`, `internal/driver/headwrite.go`),
  since every removal of the version takes them back: the removal after a
  completion or a link refused, and the unlink of a link its condition
  voided (exclusion "rgw-go removes the instance a failed versioned write
  made"). radosgw's order would have such a removal take back what was never
  added. A version that stays after its completion or its link failed, the
  outcome unknown, stays counted, as its index entry may land. The S3 ops
  refuse a versioned write until versioning is served.
- **zlib's compressor message states the window rgw-go deflates with.**
  radosgw's zlib compressor deflates with `compressor_zlib_winsize` and
  stores that value as each object's `compressor_message`
  ([ZlibCompressor.cc:94-100](https://github.com/ceph/ceph/blob/v19.2.7/src/compressor/zlib/ZlibCompressor.cc#L94-L100),
  [ZlibCompressor.cc:106-112](https://github.com/ceph/ceph/blob/v20.2.4/src/compressor/zlib/ZlibCompressor.cc#L106-L112)).
  Go's deflate always uses a 32 KiB window, which a narrower window bits
  value cannot inflate, so rgw-go stores the option's framing at 15 bits:
  -15 for a negative value (raw deflate), 15 for 8 to 15 (the zlib
  wrapper) and 31 for 16 and above (gzip) (`internal/compression`,
  `newZlibEncoder`). At the default, -15, and at 15 and 31, the message is
  radosgw's. Under any other value it differs, and where `deflateInit2`
  refuses the value, -8 to -1, 16 to 24 and 32, radosgw stores the object
  uncompressed while rgw-go compresses it. Each gateway reads the other's
  objects, except those radosgw writes at 8, which neither reads
  (`docs/ceph-upstream-bugs.md`,
  "[compressor_zlib_winsize 8 writes zlib blocks radosgw cannot read
  back](ceph-upstream-bugs.md#compressor_zlib_winsize-8-writes-zlib-blocks-radosgw-cannot-read-back)").
  Under such a value a multipart upload whose parts come from both gateways
  cannot complete: completion, on either gateway, refuses a part whose
  compression or compressor message differs from the parts before it with
  400 InvalidPart
  ([rgw_sal_rados.cc:3535-3547](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L3535-L3547),
  [rgw_sal_rados.cc:4379-4391](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4379-L4391);
  `internal/driver`, `mergeCompression`). A value of 0 to 7, or a
  `compressor_zlib_level` outside -1 to 9, has both gateways store the
  object uncompressed; rgw-go logs such options at startup.
- **`random` and a codec rgw-go has no encoder for store the object
  uncompressed.** A storage class whose `compression_type` is `random`, or a
  codec other than the four rgw-go encodes (snappy, zlib, zstd and lz4), has
  rgw-go store a PUT's, a part's or a streamed copy's data uncompressed,
  without the incompressible hint, and log the type the first time it
  meets it; a streamed copy of a compressed source, which rgw-go copies as
  it is, goes unhinted there too. radosgw compresses with the plugin it
  loads: for `random`, a teuthology setting, a codec it picks at random for
  each object, and by the upload id's hash for an upload's parts
  ([rgw_op.cc:4087-4108](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4087-L4108),
  [rgw_op.cc:4296-4317](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4296-L4317));
  for another name, such as brotli in a build with that plugin, its codec.
  A Tentacle radosgw's streamed copy loads the plugin the same way and
  marks the copy compressed, so it hints every stripe
  ([rgw_op.cc:5826-5838](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5826-L5838));
  Squid's copy loads none.
  Where no plugin loads, radosgw stores the object uncompressed too
  ([rgw_op.cc:4360-4366](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4360-L4366),
  [rgw_op.cc:4576-4582](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4576-L4582)).
  rgw-go reads no codec but the four. So an upload whose parts come from both
  gateways fails completion with 400 InvalidPart wherever radosgw compressed
  its parts, as completion on either gateway refuses parts that differ in
  their compression (the zlib entry above cites it).
- **A compressed part is hinted incompressible.** rgw-go writes every stripe
  of a part into a class whose codec it loads with the INCOMPRESSIBLE alloc
  hint, a part stored as it is after its first block fails or under options
  zlib refuses included, as both gateways write a compressed PUT's tails and
  head. radosgw writes a compressed part's stripes without it: its writer
  looks the flag up on the part head, which nothing marks
  (`docs/ceph-upstream-bugs.md`, "[radosgw
  writes a compressed part's stripes without the incompressible
  hint](ceph-upstream-bugs.md#radosgw-writes-a-compressed-parts-stripes-without-the-incompressible-hint)").
  Only BlueStore reads the hint: on a pool whose compression mode is
  aggressive it then does not compress the part's stored bytes again
  ([BlueStore.cc:17267-17272](https://github.com/ceph/ceph/blob/v19.2.7/src/os/bluestore/BlueStore.cc#L17267-L17272),
  [BlueStore.cc:17508-17513](https://github.com/ceph/ceph/blob/v20.2.4/src/os/bluestore/BlueStore.cc#L17508-L17513)).
- **A checksum algorithm rgw-go does not compute is refused at write.** In
  the op layer, which the S3 handler does not reach yet ("Client checksums
  are ignored until phase 2"), rgw-go computes CRC32, CRC32C, CRC64NVME,
  SHA1, SHA256 and SHA512. A PutObject or UploadPart that names MD5,
  XXHASH64, XXHASH128, XXHASH3, Tentacle's XXH3 or BLAKE3, in any case, in
  `x-amz-checksum-algorithm`, `x-amz-sdk-checksum-algorithm`, an
  `x-amz-checksum-<alg>` header or `x-amz-trailer` answers 400
  InvalidArgument before its body is read, on both releases, and a
  CreateMultipartUpload on Tentacle answers the same ("CreateMultipartUpload
  refuses the checksum algorithms rgw-go does not compute"). MD5, XXHASH64
  and XXHASH128 are main's types 9 to 11
  ([rgw_cksum.h:41-56](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L41-L56)
  at main), for which a v20.2.4 radosgw has no id: it indexes its
  nine-entry table with a record's stored type
  ([rgw_cksum.h:88-99](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L88-L99),
  [rgw_cksum.h:147-149](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L147-L149);
  `docs/ceph-upstream-bugs.md`, "[radosgw's checksum decode overruns its
  digest buffer and its type
  table](ceph-upstream-bugs.md#radosgws-checksum-decode-overruns-its-digest-buffer-and-its-type-table)").
  A Tentacle radosgw answers the same 400 InvalidArgument when an algorithm
  header names MD5, XXHASH64, XXHASH128 or XXHASH3, as for any name it does
  not know: its table calls type 3 xxh3
  ([rgw_cksum.h:293-300](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L293-L300),
  [rgw_cksum_pipe.cc:40-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73),
  [rgw_op.cc:4601-4611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611)).
  So the two gateways differ for those names only as a value header or a
  trailer, which radosgw ignores, and on CreateMultipartUpload. A Tentacle
  radosgw computes and stores the checksums it names XXH3 and BLAKE3, which
  rgw-go does not: phase 2 takes no XXH3 or BLAKE3 dependency, though
  XXHASH3 is a type AWS defines
  ([rgw_cksum.h:93-107](https://github.com/ceph/ceph/blob/f6f2529dd9efccf7710c8a2b561e5a2ab9d48bd3/src/rgw/rgw_cksum.h#L93-L107)
  at main). Squid's radosgw has no checksum code and ignores every such
  header. An UploadPart, an UploadPartCopy or a CompleteMultipartUpload of
  an upload whose stored type rgw-go does not compute, xxh3 or blake3 from
  a Tentacle radosgw or a type above 8 from a newer one, answers 501
  NotImplemented with nothing written, whatever the request names, the copy
  before it reads anything of its source's data (`CopyPart`,
  `internal/driver/mp_part.go`). A Tentacle radosgw serves such an upload,
  and checksums each of its parts, a copied one included, with the upload's
  type and flags
  ([rgw_op.cc:4471-4472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472),
  [rgw_op.cc:4601-4645](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4645)
  and
  [rgw_cksum_digest.h:97-128](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_digest.h#L97-L128)
  at v20.2.4). The RADOS driver's GetUpload reads an upload's checksum type
  from its `multipart_upload_info`, so these 501s are answered on Tentacle,
  in the binary too, which needs no checksum header to meet them
  (`internal/driver/mp_cksum_test.go`). A Squid cluster's uploads have no
  checksum type.
- **A checksum value without an algorithm header names its algorithm and is
  verified.** In the op layer, which the S3 handler does not reach yet
  ("Client checksums are ignored until phase 2"), a PutObject or UploadPart
  without `x-amz-checksum-algorithm` or `x-amz-sdk-checksum-algorithm`
  takes its algorithm from its one `x-amz-checksum-<alg>` header, or from
  the one checksum trailer `x-amz-trailer` lists, and rgw-go verifies the
  value, answering 400 BadDigest when the body's checksum differs, as
  Amazon S3 does, and when a declared trailer brings no value ("A declared
  checksum trailer that brings no value is refused"). A Tentacle radosgw
  builds no checksum filter for such a request, unless it is a part of an
  upload with a checksum type, so it computes and stores no checksum and
  never compares the value
  ([rgw_cksum_pipe.cc:40-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73);
  `docs/ceph-upstream-bugs.md`, "[radosgw verifies no checksum value sent
  without an algorithm
  header](ceph-upstream-bugs.md#radosgw-verifies-no-checksum-value-sent-without-an-algorithm-header)").
  Before the body rgw-go also answers 400 InvalidRequest for two values,
  whether two `x-amz-checksum-<alg>` headers, two checksum trailers or one
  of each, and for a value header or trailer of another algorithm than the
  algorithm header's. A Tentacle radosgw computes the algorithm header's
  checksum and compares it only with that algorithm's own value, a header
  or a trailer, ignoring any other, and stores it unverified when that
  algorithm has none
  ([rgw_cksum_pipe.h:193-211](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L193-L211)).
  Squid's radosgw ignores them all.
- **An UploadPart naming another algorithm than its upload's is refused.**
  In the op layer, which the S3 handler does not reach yet ("Client
  checksums are ignored until phase 2"), on Tentacle, an UploadPart whose
  algorithm header, value header or checksum trailer names another
  algorithm than its upload's checksum type answers 400 InvalidRequest
  before its body is read, and stores no part. A Tentacle radosgw computes
  an algorithm header's type over such a part, the header winning over the
  upload's type
  ([rgw_cksum_pipe.cc:40-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73)),
  and then refuses the upload's completion with 400 InvalidRequest
  ([rgw_op.cc:7121-7131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7121-L7131)),
  so the upload completes only once the part is uploaded again. For a value
  header or a trailer of another algorithm without an algorithm header, it
  computes the upload's type, unverified, and ignores the value. A Squid
  upload has no checksum type.
- **Squid clusters get single-object and part checksums, not an upload's.**
  In the op layer, which the S3 handler does not reach yet ("Client
  checksums are ignored until phase 2"), rgw-go reads a PutObject's and an
  UploadPart's checksum request on a Squid cluster as on Tentacle: it
  refuses what it cannot compute or verify, verifies a value, answering 400
  BadDigest when it differs, and stores the checksum in the head's
  `user.rgw.cksum`, a part head's included. Squid's radosgw has no checksum
  code, ignores every checksum header and never reads the attr, which is
  one of its own (spec §8). CreateMultipartUpload's
  `x-amz-checksum-algorithm`, `x-amz-sdk-checksum-algorithm` and
  `x-amz-checksum-type` are ignored on Squid, with no error, as Squid's
  radosgw ignores them, since its `multipart_upload_info` and part record
  have no field for a checksum
  ([rgw_common.h:1508-1552](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.h#L1508-L1552),
  [rgw_basic_types.h:253-300](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_basic_types.h#L253-L300)).
  A Squid upload therefore has no checksum type: a part naming no algorithm
  computes none, a part's own checksum carries no upload flags, and a
  completion ignores a supplied value and stores no combined checksum.
- **CreateMultipartUpload refuses the checksum algorithms rgw-go does not
  compute.** In the op layer, which the S3 handler does not reach yet
  ("Client checksums are ignored until phase 2"), on Tentacle, a
  CreateMultipartUpload whose algorithm header names MD5, XXHASH64,
  XXHASH128, XXHASH3, XXH3 or BLAKE3, in any case, or that carries an
  `x-amz-checksum-<alg>` header or an `x-amz-trailer` naming one, answers
  400 InvalidArgument and creates no upload. A Tentacle radosgw reads only
  the algorithm header there, takes a name it does not know, MD5, XXHASH64,
  XXHASH128 and XXHASH3 among them, for no algorithm, without an error, and
  creates an XXH3 or BLAKE3 upload
  ([rgw_rest_s3.cc:4507-4521](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4507-L4521),
  [rgw_cksum_pipe.h:84-94](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L84-L94)).
  rgw-go refuses them as PutObject and UploadPart do, so that a client
  asking for MD5 is not handed an upload that keeps no checksum, and none
  an upload whose every part rgw-go answers with 501. A name that is no
  algorithm at all, an empty one included, is no algorithm on rgw-go too.
- **UploadPartCopy reads no checksum request header.** rgw-go's
  UploadPartCopy reads none of `x-amz-checksum-algorithm`,
  `x-amz-sdk-checksum-algorithm`, `x-amz-checksum-<alg>` and
  `x-amz-trailer`, as AWS's API model defines no checksum request header
  for it: aws-sdk-go v1.55.8's `UploadPartCopyInput` has none, where
  `UploadPartInput` has `x-amz-sdk-checksum-algorithm` and
  `x-amz-checksum-*`. On Tentacle the copied part takes its upload's
  checksum type and flags, which the RADOS driver's GetUpload reads from
  the upload's `multipart_upload_info`. A v20.2.4 radosgw honours the
  headers: its filter reads the copy request's own
  headers, and an algorithm header wins over the upload's type
  ([rgw_op.cc:4601-4611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611),
  [rgw_cksum_pipe.cc:40-73](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.cc#L40-L73)).
  It refuses a name it does not know with 400 InvalidArgument, verifies a
  value header, so it can answer 400 BadDigest to a header the model does
  not define for the operation
  ([rgw_op.cc:4759-4790](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4759-L4790)),
  and lets a copy part store a type its completion then refuses with 400
  InvalidRequest
  ([rgw_op.cc:7121-7131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7121-L7131)).
  v19.2.7 computes nothing for a copy part.
- **A declared checksum trailer that brings no value is refused.** In the
  op layer, which the S3 handler does not reach yet ("Client checksums are
  ignored until phase 2"), a PutObject or UploadPart whose checksum value
  `x-amz-trailer` declares answers 400 BadDigest, storing nothing, when the
  value does not arrive. rgw-go keeps trailers only for an aws-chunked
  payload whose `x-amz-content-sha256` ends in `TRAILER` ("aws-chunked
  trailer sections are read line by line"), so every other payload, the
  plain `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` and a single payload among
  them, is refused, and so is a `TRAILER` payload whose section lacks the
  declared trailer. A Tentacle radosgw takes the listed trailers from any
  aws-chunked payload's section, as that entry cites, and verifies against
  them the checksum an algorithm header, or a part's upload type, names:
  the filter `RGWPutObj_Cksum::Factory` builds
  ([rgw_op.cc:4601-4611](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4601-L4611)),
  for a part with no algorithm header from the upload's
  `multipart_cksum_type`
  ([rgw_op.cc:4471-4472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4471-L4472)),
  reads its value through `RGWPutObj_Cksum::expected`
  ([rgw_cksum_pipe.h:193-200](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L193-L200)).
  Where no value is there, radosgw stores that checksum unverified
  ([rgw_op.cc:4757-4790](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4757-L4790)),
  and with neither an algorithm header nor an upload type it computes none.
- **CompleteMultipartUpload reads one checksum header, of any type.** In
  the op layer, which the S3 handler does not reach yet ("Client checksums
  are ignored until phase 2"), rgw-go takes the value a completion of an
  upload with a checksum type supplies from its one `x-amz-checksum-<alg>`
  header, of every type, `crc64nvme` included, and compares it with the
  upload's combined checksum on Tentacle; a value of another type than the
  upload's, `x-amz-checksum-md5` and the other names with no Tentacle type
  among them, is 400 BadDigest, and two such headers are 400
  InvalidRequest, with nothing written. A Tentacle radosgw takes the first
  header it finds from `crc32` to `blake3`, so it never reads `crc64nvme`
  or main's types, compares that one with the combination, and ignores the
  others
  ([rgw_cksum_pipe.h:120-136](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum_pipe.h#L120-L136),
  [rgw_op.cc:7295-7341](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7295-L7341)).
  A completion of an upload without a checksum type, every Squid upload
  among them, reads no checksum header, as radosgw compares none for it.
- **CompleteMultipartUpload combines the parts it completes and compares the
  whole supplied checksum.** On Tentacle, a completion of an upload with a
  checksum type combines the checksums of exactly the parts it names, in
  number order, from the part records its assembly matched to them, once
  every named part has matched an uploaded one, and holds a supplied value
  to the whole of the combination (`combineChecksums`,
  `internal/driver/mp_cksum.go`). radosgw differs in three ways; in each,
  every refusal writes nothing on either gateway:
  - radosgw's `try_sum_part_cksums` combines the last page `list_parts`
    returns for the request's part count rather than the parts the request
    names
    ([rgw_op.cc:7068-7092](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7068-L7092),
    [rgw_sal_rados.cc:4193](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L4193)),
    and the completion runs it before `upload->complete` matches those parts
    ([rgw_op.cc:7273-7282](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7273-L7282),
    [rgw_op.cc:7366-7369](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7366-L7369);
    `docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload
    combines the checksums of the parts it lists, not of those it
    completes](ceph-upstream-bugs.md#radosgws-completemultipartupload-combines-the-checksums-of-the-parts-it-lists-not-of-those-it-completes)").
    So rgw-go answers a completion's part faults, 400 InvalidPart and
    EntityTooSmall, before its checksum faults, 400 InvalidRequest and
    BadDigest. A completion naming fewer or other parts than were uploaded
    is refused with InvalidPart by both gateways in the end, but radosgw
    first answers 400 InvalidRequest when a part of its last page has no
    checksum or one of another type, and 400 BadDigest when a supplied value
    does not match that page's combination. A part uploaded while radosgw's
    completion runs, after its combination and before `complete` lists the
    parts, can leave a wrong checksum on the object; rgw-go combines the
    very records its assembly matched, from one listing.
  - radosgw accepts a supplied value that only starts with its armored
    value, or with a composite's armor less its part count, so one with a
    trailing byte, a composite's with another part count and a full-object
    CRC's with a part count pass
    ([rgw_op.cc:7317-7336](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7317-L7336);
    `docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload
    accepts a supplied checksum that only starts with its
    own](ceph-upstream-bugs.md#radosgws-completemultipartupload-accepts-a-supplied-checksum-that-only-starts-with-its-own)").
    rgw-go accepts the armored value, which ends in `-<parts>` for a
    composite, or a composite's armor alone, the form radosgw's comment
    accepts for minio's clients, and answers 400 BadDigest to any other.
  - radosgw compares no empty value, where rgw-go answers 400 BadDigest, and
    never reads `x-amz-checksum-crc64nvme`, so it stores a CRC64NVME
    upload's combination whatever value is supplied, where rgw-go compares
    it ("CompleteMultipartUpload reads one checksum header, of any type";
    `docs/ceph-upstream-bugs.md`, "[radosgw's CompleteMultipartUpload never
    compares a supplied CRC64NVME
    checksum](ceph-upstream-bugs.md#radosgws-completemultipartupload-never-compares-a-supplied-crc64nvme-checksum)").

  The S3 handler does not yet hand the op a completion's checksum header,
  so in the binary rgw-go compares no supplied value ("Client checksums are
  ignored until phase 2"); the combination and the order of the faults
  hold in the binary. A Squid upload has no checksum type, so nothing is
  combined or compared on Squid ("Squid clusters get single-object and part
  checksums, not an upload's").
- **A copy that streams its data drops a composite checksum.** A CopyObject
  under `COPY` keeps its source's `user.rgw.cksum` and one under `REPLACE`
  drops it, as `set_copy_attrs` does
  ([rgw_rados.cc:3702-3732](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3702-L3732),
  [rgw_rados.cc:3855-3885](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3855-L3885)),
  and rgw-go computes no checksum for a copy, as Tentacle's radosgw computes
  none
  ([rgw_rados.cc:5381-5385](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5381-L5385))
  and Squid's has no checksum code. Where the copy cannot share its
  source's tails and streams the data, which leaves it without parts,
  rgw-go also drops a record that combines part checksums: a record flagged
  COMPOSITE and COMBINED, on any source, or one on a source with parts that
  carries neither V2 nor FULL_OBJECT, which `get_checksum_type` reads as a
  composite written before CRC combining
  ([rgw_cksum.h:382-399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_cksum.h#L382-L399)).
  A radosgw of either release carries such a record to the copy, where a
  Tentacle radosgw's checksum-mode GET and GetObjectAttributes show it as
  the copy's FULL_OBJECT checksum, which a composite's value is not
  (`docs/ceph-upstream-bugs.md`, "[radosgw carries a composite checksum to
  a copy without parts, which Tentacle reports as its FULL_OBJECT
  checksum](ceph-upstream-bugs.md#radosgw-carries-a-composite-checksum-to-a-copy-without-parts-which-tentacle-reports-as-its-full_object-checksum)"). A
  full-object CRC rides the copy, a one-part upload's without V2 included
  (`docs/ceph-upstream-bugs.md`, "[radosgw reports a single-part CRC
  upload's full-object checksum as
  COMPOSITE](ceph-upstream-bugs.md#radosgw-reports-a-single-part-crc-uploads-full-object-checksum-as-composite)").
  An attr rgw-go cannot read whole, a record that does not decode or one
  with bytes past it (`cksum.DecodeAttr`), is dropped too: radosgw carries
  it, and its readers of the copy read it unsafely or each their own way
  (`docs/ceph-upstream-bugs.md`, "[radosgw's checksum decode overruns its
  digest buffer and its type
  table](ceph-upstream-bugs.md#radosgws-checksum-decode-overruns-its-digest-buffer-and-its-type-table)"
  and "[radosgw aborts on a checksum-mode GET of an object whose checksum
  attr has bytes past its
  record](ceph-upstream-bugs.md#radosgw-aborts-on-a-checksum-mode-get-of-an-object-whose-checksum-attr-has-bytes-past-its-record)").
  One record is dropped though
  its value is the copy's CRC: a one-part CRC upload a Tentacle radosgw
  completed as COMPOSITE, which keeps the part's own CRC under the composite
  flags (`docs/ceph-upstream-bugs.md`, "[radosgw stores a COMPOSITE CRC
  upload's checksum as a full-object
  CRC](ceph-upstream-bugs.md#radosgw-stores-a-composite-crc-uploads-checksum-as-a-full-object-crc)").
  A copy that shares its source's tails keeps the record, and a multipart
  source's parts with it.
- **An attr write leaves the OLH attrs as they are on disk.** radosgw's
  PutObjectTagging, PutObjectAcl, PutObjectRetention and
  PutObjectLegalHold write the object's whole attrset back through
  `modify_obj_attrs`, following the key's OLH
  ([rgw_sal_rados.cc:2402-2420](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_sal_rados.cc#L2402-L2420),
  [rgw_sal_rados.cc:2994-3012](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_sal_rados.cc#L2994-L3012)).
  When the current version is the null version, that attrset is the OLH
  head's, `user.rgw.olh.ver`, `olh.info` and any pending attrs included,
  and the write puts them back as read, guarded only by the write tag,
  which the OLH protocol's head ops never change; a link or delete marker
  applied between the read and the write is undone on the head
  (`docs/ceph-upstream-bugs.md`, "[radosgw's attr writes put a null-current OLH head's OLH attrs back, reverting a link that lands in between](ceph-upstream-bugs.md#radosgws-attr-writes-put-a-null-current-olh-heads-olh-attrs-back-reverting-a-link-that-lands-in-between)").
  rgw-go's `SetObjectAttrs` (`internal/driver/attrs.go`) writes and
  removes no `user.rgw.olh.` attr: they stay as the OLH protocol left them,
  and the attrs asked for land as radosgw's do. The state written is the
  one radosgw leaves when nothing lands in between.
- **The removal of an OLH head's pending attrs keeps the head's mtime.**
  radosgw's `olh_cancel_modification`, which removes a link's or an
  unlink's pending attr once its index op failed, and
  `remove_olh_pending_entries`, which removes those older than
  `rgw_olh_pending_timeout_sec`, write the head with no mtime
  ([rgw_rados.cc:7754-7797](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7754-L7797),
  [rgw_rados.cc:8936-8967](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8936-L8967);
  [rgw_rados.cc:8417-8460](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8417-L8460),
  [rgw_rados.cc:9682-9713](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9682-L9713)),
  so the OSD stamps the head with the gateway's clock, and the null version,
  whose data the head holds, reports that time as its Last-Modified
  (`docs/ceph-upstream-bugs.md`, "[radosgw's removal of an OLH head's
  pending attr moves the null version's
  mtime](ceph-upstream-bugs.md#radosgws-removal-of-an-olh-heads-pending-attr-moves-the-null-versions-mtime)").
  rgw-go's two writes send the zero mtime, for which the OSD keeps the
  object's stored mtime
  ([PrimaryLogPG.cc:8978-8984](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L8978-L8984),
  [PrimaryLogPG.cc:9084-9090](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L9084-L9090)),
  under radosgw's OLH tag guard alone, so the head keeps the mtime it holds
  when the write lands, a null write's that landed after the read included
  (`keepHeadMtime`, `internal/driver/olh_head.go`). rgw-go also removes a pending attr sooner
  than radosgw does: a link or unlink whose replay finds nothing under its
  own op tag, as the removal of a noncurrent delete marker's does, removes
  its attr at once (`finishOLHModification`, `internal/driver/olh.go`),
  where radosgw's replay of an empty log returns at once
  ([rgw_rados.cc:8423-8425](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8423-L8425),
  [rgw_rados.cc:9169-9171](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9169-L9171))
  and leaves the attr for a later read to expire, moving the mtime then. A
  null version's mtime that a radosgw gateway moved stays where it moved.

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
- **A versioned write awaits its index completion.** radosgw finishes a
  write's bucket index entry with a `bucket_complete_op` it sends with
  `aio_operate` and does not wait for, and a versioned write then links
  the version under its name's OLH entry
  ([rgw_rados.cc:3352-3375](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3352-L3375),
  [rgw_rados.cc:9714](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9714);
  [rgw_rados.cc:3473-3496](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3473-L3496),
  [rgw_rados.cc:10450](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10450)).
  librados keeps one client's operations on one object in the order they
  were sent, so the completion reaches the index shard before the link,
  which reads the instance entry the completion wrote
  ([cls_rgw.cc:1670](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1670),
  [cls_rgw.cc:1811](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1811)). rgw-go's
  completions reach a shard in no set order (`docs/cgo-limitations.md`,
  "Bucket index completions to one shard can land out of order"). A link
  that landed first would list the version from its pending instance
  entry, with no size or ETag, and the completion would then reset that
  entry's flags to `FLAG_VER` alone, dropping its current flag
  ([cls_rgw.cc:1064](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1064),
  [cls_rgw.cc:1199](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1199)). So rgw-go's
  versioned write sends its ADD completion and waits for the class's
  answer before it links (`indexOp.completeSync`,
  `internal/driver/indexop.go`). The request is radosgw's, behind the
  reshard guard: a refusal from a resharding shard is waited out and the
  completion sent again to the shard the bucket then names, and a shard
  that is gone is found again from the bucket. The completion manager
  resends a refused completion without its object locator, as radosgw's
  does; the awaited completion keeps it, which changes nothing stored once
  the prepare has made the entry. Any other failure, the class's or the
  guard's, is the write's to answer, where radosgw's unawaited completion
  leaves the pending entry for listing to reconcile. An error does not
  prove the completion did not land, since one that a context end or a
  timeout interrupted can land later, so the write never links after one.
  An unversioned write of an object's plain or null entry on a bucket whose
  flags carry `BUCKET_VERSIONED`, a suspended bucket's write of a key with
  no OLH, and an attr write of such an entry, PutObjectTagging's,
  PutObjectAcl's or PutObjectRetention's, await their completion too before
  they answer (`completePlain`, `internal/driver/headwrite.go`, which
  `SetObjectAttrs` also calls): the key's next delete links the null
  delete marker, which takes the pending op of the null entry it replaces
  ([cls_rgw.cc:1349-1357](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1349-L1357),
  [cls_rgw.cc:1476-1484](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1476-L1484)),
  and a completion landing after that link clears the op from the null
  data key alone, leaving the marker pending for good
  (`docs/ceph-upstream-bugs.md`, "[radosgw's listing repair of a null
  version's list entry overwrites or removes the key's version
  marker](ceph-upstream-bugs.md#radosgws-listing-repair-of-a-null-versions-list-entry-overwrites-or-removes-the-keys-version-marker)").
  Its failure is logged and left for listing to reconcile, as the unawaited
  completion's is, and the wait outlives its request's end, as the
  completion manager's send does. Every other unversioned write, a
  namespaced one such as a multipart part's or a named version's attr write
  included, keeps radosgw's unawaited completion. The cost is a round trip
  to the index shard per versioned write, and per such write or attr write,
  that radosgw does not wait for, though the OSD applies the two operations
  in turn either way.
  The driver's versioned PutObject, CopyObject and CompleteMultipartUpload
  await it (`linkVersion`, `internal/driver/put_versioned.go`). The S3 ops
  refuse a versioned write until versioning is served.
- **An OLH replay awaits the index completion of each version it
  removes.** radosgw's `apply_olh_log` removes each version its log names
  through the unversioned delete, whose `bucket_complete_op` it sends with
  `aio_operate` and does not wait for, then writes the OLH head and trims
  the log, or clears the OLH when no version is left
  ([rgw_rados.cc:8530-8563](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8530-L8563),
  [rgw_rados.cc:9714](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9714);
  [rgw_rados.cc:9275-9309](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L9275-L9309),
  [rgw_rados.cc:10450](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10450)).
  librados keeps the completion ahead of the trim or the clear, which go to
  the same shard. The clear removes the version marker, the plain entry
  through which the class reads the null version's instance entry
  ([cls_rgw.cc:2165-2187](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2165-L2187),
  [cls_rgw.cc:946-978](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L946-L978);
  [cls_rgw.cc:2330-2352](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2330-L2352),
  [cls_rgw.cc:1076-1108](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1076-L1108)),
  so a completion of the null version's removal that reached the shard
  after it would find no entry pending its tag and be refused with EINVAL
  before it unaccounts the version
  ([cls_rgw.cc:1047-1073](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1047-L1073),
  [cls_rgw.cc:1182-1208](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1182-L1208)),
  leaving the instance entry stored as existing and counted in the shard
  header's stats. rgw-go's completions reach a shard in no set order
  (`docs/cgo-limitations.md`, "Bucket index completions to one shard can
  land out of order"), so its replay sends each removal's DEL completion on
  the reshard-guarded path and waits for the class's answer before its head
  op (`indexOp.completeDelSync`, `internal/driver/indexop.go`). A failure
  is logged and the replay goes on, as radosgw's completion manager drops
  one
  ([rgw_rados.cc:1011-1017](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1011-L1017),
  [rgw_rados.cc:1052-1058](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1052-L1058)).
  Every removal waits, not the null version's alone, so a removed version's
  instance entry is gone when the delete that replayed it returns, as
  radosgw's is by the time its next operation on the shard lands. The cost
  is a round trip to the index shard per version a replay removes. radosgw
  itself can still lose the race while the bucket reshards
  (`docs/ceph-upstream-bugs.md`, "radosgw's null version removal loses its
  index completion to the OLH clear while the bucket reshards").
- **An index read of a versioned key that finds its shard gone reads the
  shard the bucket now names.** radosgw reads a name's pending OLH log from
  the shard its cached bucket instance names, and `bi_get` of the name's OLH
  entry or of a version's entry from the shard its caller's bucket info
  names, once each
  ([rgw_rados.cc:8237-8256](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8237-L8256),
  [rgw_rados.cc:9490-9501](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9490-L9501);
  [rgw_rados.cc:8983-9002](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L8983-L9002),
  [rgw_rados.cc:10239-10250](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10239-L10250)).
  A shard object a reshard removed answers ENOENT, which `bi_get` also
  answers for an entry that is not there. rgw-go keeps the shard its
  request found, so on ENOENT from any of these reads it reads the bucket
  instance again,
  past its metadata cache, and when that puts the name on another shard
  reads there, within ten tries (`indexOp.readShard`,
  `internal/driver/indexop.go`); only an ENOENT from the shard the bucket
  now names is the answer. So a versioned write or delete whose bucket a
  reshard committed under it reads the log and the OLH entry from the new
  shard, where radosgw, reading through bucket info that still names the
  old shard, answers ENOENT; and rgw-go never takes a removed shard's
  ENOENT for an absent OLH entry, which would settle a refused version
  removal as final while its log entries sit on the new shard (the
  exclusion "A replay passes over a version removal that the writer's
  expected write tag refuses"). rgw-go resolves a version's entry the
  same way: its one to three reads, in the class's `read_key_entry` order,
  start again from the plain key on the shard the bucket now names
  (`getInstanceEntry`, `internal/driver/olh_index.go`), where radosgw's
  `bi_get_instance`, which its versioned delete alone calls, answers the
  removed shard's ENOENT
  ([rgw_rados.cc:5908](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5908),
  [rgw_rados.cc:6520](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6520)). rgw-go
  decides on that answer where radosgw makes no such decision: whether to
  put back the list entry of a version whose removal was refused, whether a
  named version without a head is a delete marker, which S3 answers 405,
  or missing, and whether a listing's null version is a marker. A stale
  shard's not-found would put each wrong. Each such ENOENT, that of an
  entry that is not there included, costs a read of the bucket instance.
  The reads of a versioned key are served and its writes are not; a named
  delete marker and a missing version both answer 404 until Task V14a
  sends the 405 (`docs/ceph-upstream-bugs.md`,
  "[radosgw answers a GET or HEAD naming a delete marker's version 404 NoSuchKey](ceph-upstream-bugs.md#radosgw-answers-a-get-or-head-naming-a-delete-markers-version-404-nosuchkey)").

### Bucket listing differences

rgw-go lists a bucket, in ListObjects v1 and v2 and in the versions
listing, as radosgw's `RGWListBucket` and `RGWRados::Bucket::List` do at
the v19.2.6 and v20.2.4 tags. Where the two differ, rgw-go does the
following.

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
- **ListMultipartUploads keeps the common prefixes radosgw drops.** radosgw
  lists uploads through the bucket listing with `MultipartMetaFilter` as its
  `access_list_filter`, which it applies before the delimiter
  (`driver/rados/rgw_sal_rados.cc:931` and
  `driver/rados/rgw_rados.cc:2003-2009` at v19.2.6, `:951` and `:2106-2112`
  at v20.2.4). cls_rgw's `bucket_list` has already rolled every name holding
  the delimiter into one common-prefix entry (`cls/rgw/cls_rgw.cc:625-663`
  at v19.2.6, `:675-713` at v20.2.4), whose name ends in the delimiter and
  so fails the filter's `.meta` test, unless the delimiter is a suffix of
  `.meta`, such as `a`, or ends in `.meta`: then a prefix whose name passes
  the filter, such as a whole meta name `<key>.<id>.meta`, is listed as a
  common prefix (`:2063` and `:2076` at v19.2.6, `:2166` and `:2179` at
  v20.2.4). With any other delimiter, radosgw's ListMultipartUploads lists
  no CommonPrefixes and none of the uploads under them
  (`docs/ceph-upstream-bugs.md`, "radosgw's ListMultipartUploads drops its
  common prefixes and the uploads under them"). rgw-go applies the filter
  only to a name it would list as an upload, so it lists those common
  prefixes; one formed only by part heads, left behind by an upload whose
  meta object is gone, is listed too and holds no upload. Each common prefix
  counts toward max-uploads, as in ListObjects, so a delimited page holds
  fewer uploads than radosgw's for the same request. A truncated page whose
  last counted item is a common prefix names the prefix as NextKeyMarker
  with no NextUploadIdMarker: the marker `<prefix>..meta` falls inside the
  prefix, and the next page's listing skips past it. radosgw's next markers
  are those of the page's last upload, or none when it holds no upload, so
  on such a page, which it gives only with such a delimiter, its next page
  repeats the prefixes or starts over, and a client paging it may never end;
  rgw-go's markers always move past the page.
- **Listing-time suggestions are bounded.** radosgw sends each shard's
  `dir_suggest_changes` with `aio_operate` and never waits for it
  (`:9909` and `:10144` at v19.2.6, `:10831` and `:11068` at v20.2.4).
  rgw-go sends them in the background too and never waits either, but
  keeps at most `rgw_bucket_index_max_aio` in flight, each for at most 30
  seconds; a shard's suggestions that find no free slot are dropped, and the
  next listing of the same entries suggests them again.
- **An unordered listing lists a shard that answers advance-and-retry
  again from its marker.** cls_rgw's `bucket_list` answers
  `RGWBIAdvanceAndRetryError` (-EFBIG) when eight omap reads list nothing,
  with its reply encoded and the marker to go on from in it
  ([cls_rgw.cc:680-693](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L680-L693),
  [cls_rgw.cc:730-743](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L730-L743)); a listing without
  versions meets it on a run of delete markers and noncurrent versions
  longer than those reads. radosgw's ordered listing lists the shard again
  from the marker
  ([cls_rgw_client.cc:99-110](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L99-L110),
  [cls_rgw_client.cc:161-166](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L161-L166);
  [svc_bi_rados.cc:948-968](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L948-L968)),
  and so does rgw-go's. radosgw's unordered listing reads each shard with
  `rgw_rados_operate` and fails with the -EFBIG, 500 UnknownError
  ([rgw_rados.cc:10264-10272](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10264-L10272),
  [rgw_rados.cc:10990-10998](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10990-L10998);
  `docs/ceph-upstream-bugs.md`, "radosgw's unordered listing fails on a
  shard that answers advance-and-retry"). rgw-go's lists the shard again
  from the marker, as does DeleteBucket's emptiness check, which lists
  through it. A marker past a name's versions, the name and 0x01, is also
  the key of an object named so, which a read after it leaves out
  (`docs/ceph-upstream-bugs.md`, "cls_rgw's listing can skip an object
  whose name is another versioned name and 0x01"), so rgw-go lists such a
  shard again after the name, NUL and `w` instead, which sorts after every
  list key of the name and before the object. A reply whose marker is
  empty or would start the shard again where it started fails either
  listing with 500 UnknownError, where radosgw's ordered
  listing lists the shard again, from that start key without end on
  Tentacle ([svc_bi_rados.cc:954-956](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L954-L956))
  and from the shard's first key on Squid
  ([cls_rgw_client.cc:351-370](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw_client.cc#L351-L370)).
- **ListObjectVersions URL-encodes KeyMarker, Prefix and Delimiter.**
  Under URL encoding radosgw's versions document writes `Prefix` and
  `Delimiter` with `dump_string`, as sent
  ([rgw_rest_s3.cc:1787-1791](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1787-L1791),
  [rgw_rest_s3.cc:1890-1894](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1890-L1894)),
  and `KeyMarker` with `dump_string` too, where `NextKeyMarker` goes
  through `dump_urlsafe`
  ([rgw_rest_s3.cc:1815-1818](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1815-L1818),
  [rgw_rest_s3.cc:1918-1921](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1918-L1921);
  `docs/ceph-upstream-bugs.md`, "radosgw's bucket listings leave elements
  S3 encodes unencoded under encoding-type=url"). S3 encodes all five, "KeyMarker, NextKeyMarker, Prefix, Key, and Delimiter"
  (AWS's API reference, API_ListObjectVersions.html, fetched 2026-10-22),
  and botocore, which sends `encoding-type=url` on every ListObjectVersions
  that names none, decodes them. rgw-go encodes `KeyMarker` with its slash,
  as `NextKeyMarker` is, and `Prefix` and `Delimiter` keeping theirs, as
  radosgw's `dump_urlsafe` keeps a common prefix's
  ([rgw_rest_s3.cc:1800](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1800),
  [rgw_rest_s3.cc:1903](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1903)).
  `VersionIdMarker` and `NextVersionIdMarker` are written as both gateways
  write them, never encoded.
- **ListObjects, ListObjectsV2 and ListMultipartUploads URL-encode the
  elements S3 encodes.** Under URL encoding radosgw writes the `Prefix` of
  ListObjectsV2 with `dump_string`, in the response it shares with
  ListObjects
  ([rgw_rest_s3.cc:1886](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1886),
  [rgw_rest_s3.cc:1989](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1989)),
  and its `StartAfter` with `dump_string` too
  ([rgw_rest_s3.cc:2120](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2120),
  [rgw_rest_s3.cc:2223](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2223)),
  where S3 encodes "Delimiter, Prefix, Key, and StartAfter" (AWS's API
  reference, API_ListObjectsV2.html, fetched 2026-10-22). For
  ListMultipartUploads it opens `ListMultipartUploadsResult` with no
  `EncodingType` and writes its `Prefix`, `KeyMarker`, `NextKeyMarker` and
  `Delimiter` with `dump_string`
  ([rgw_rest_s3.cc:4216-4232](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4216-L4232),
  [rgw_rest_s3.cc:4734-4750](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4734-L4750)),
  where S3 encodes "Delimiter, KeyMarker, Prefix, NextKeyMarker, Key" and
  returns `EncodingType` (API_ListMultipartUploads.html, fetched
  2026-10-22). For ListObjects it writes `Marker` with `dump_string`
  ([rgw_rest_s3.cc:1970](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1970),
  [rgw_rest_s3.cc:2073](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2073)).
  AWS's ListObjects reference names no list of encoded elements, so the
  only evidence that S3 encodes `Marker` is botocore 1.43.90's decoder,
  which, when it chose the encoding, decodes `Delimiter`, `Marker`,
  `NextMarker` and each `Key` but not the top-level `Prefix`
  (`decode_list_object`, `botocore/handlers.py`; `docs/ceph-upstream-bugs.md`,
  "radosgw's bucket listings leave elements S3 encodes unencoded under
  encoding-type=url"). rgw-go encodes each of these: ListObjectsV2's
  `Prefix` and ListMultipartUploads' four keeping their slashes, as the
  common prefixes are; `StartAfter` and ListObjects' `Marker` with theirs,
  as a `Key` and `NextMarker` are. It writes ListMultipartUploads'
  `EncodingType` first, where radosgw's other listings put it. ListObjects'
  `Prefix`, the ListObjectsV2 tokens and the upload id markers are written
  as sent. A ListMultipartUploads client that asks for the encoding and
  sends `NextKeyMarker` back without decoding it pages from the wrong key on
  rgw-go, as it would on S3, where radosgw's marker served it.
- **A versions listing starts after every version of a key-marker given
  alone.** radosgw passes a `key-marker` without a `version-id-marker` to
  the class as a start key without an instance
  ([rgw_rest_s3.cc:1758-1759](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1758-L1759),
  [rgw_rest_s3.cc:1861-1862](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1861-L1862)),
  which `encode_list_index_key` takes as the name's plain key
  ([cls_rgw.cc:360-363](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L360-L363),
  [cls_rgw.cc:410-413](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L410-L413)),
  before the name's list keys, so the page lists the key-marker's own
  versions (`docs/ceph-upstream-bugs.md`, "radosgw's ListObjectVersions
  lists the key-marker's versions again without a version-id-marker"). AWS
  starts after every version of the key: in AWS's API reference
  (API_ListObjectVersions.html, fetched 2026-10-22), the sample
  `GET /?versions&key-marker=key2` lists only the versions of `key3` and
  `sourcekey`. rgw-go starts where AWS does, after the name, NUL and `w`,
  which sorts after every list key of
  the name and before the name and 0x01
  ([cls_rgw.cc:302-323](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L302-L323),
  [cls_rgw.cc:352-373](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L352-L373)),
  so the page starts at the next name. An object named like the key, NUL
  and more that sorts at or before that start is left out; radosgw lists
  it. Under a delimiter that ends the name, NUL and `w`, such as `w`, the
  class takes that start for a common prefix and starts after it and 0xFF
  ([cls_rgw.cc:548-554](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L548-L554),
  [cls_rgw.cc:598-604](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L598-L604)),
  so the names that extend the name, NUL and `w` are left out too, where
  radosgw lists them. Either needs an object name that holds a NUL, which,
  as far as the source shows, only a browser POST's `key` can store in
  radosgw (`docs/ceph-upstream-bugs.md`,
  "radosgw's policy match cuts an object name at a NUL that its RADOS name
  keeps"). radosgw names `null` as the next `version-id-marker` for an
  entry without an instance, so a client that pages by the returned markers
  never sends a `key-marker` alone. The `list-type=2` form with `versions`
  reads no version marker: its `continuation-token`, else its
  `start-after`, is a key-marker alone
  ([rgw_rest_s3.cc:1771-1777](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1771-L1777),
  [rgw_rest_s3.cc:1874-1880](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1874-L1880)).
  rgw-go starts a `continuation-token` at its key's first version, as
  radosgw does, since the form's next token names the key a page ended
  inside: a client paging that form by its tokens lists that key's versions
  again, and never gets past a key with `max-keys` versions or more, on
  either gateway. A `start-after` starts after every version of its key, as
  S3 reads a key given alone, where radosgw lists that key's versions.
- **A versions listing resumes after the null delete marker its
  version-id-marker names.** radosgw passes a `version-id-marker` of `null`
  to the class as the instance `null`
  ([rgw_rest_s3.cc:1758-1759](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1758-L1759),
  [rgw_rest_s3.cc:1861-1862](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1861-L1862)),
  which `encode_list_index_key` looks up at the null version's data key
  alone ([cls_rgw.cc:366-381](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L366-L381),
  [cls_rgw.cc:416-431](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L416-L431)). A null
  delete marker's instance entry has a key of its own
  ([cls_rgw.cc:1399-1400](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1399-L1400),
  [cls_rgw.cc:1526-1527](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1526-L1527)), so after a
  page that ended on one, which `NextVersionIdMarker` names `null`, radosgw
  starts past every version of the key, or, while the null version the
  marker replaced awaits its removal, after that version's old list key
  ([cls_rgw.cc:1712-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1712-L1734),
  [cls_rgw.cc:1853-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1853-L1875)): the key's older
  versions are not listed (`docs/ceph-upstream-bugs.md`, "radosgw's
  ListObjectVersions skips a key's older versions after a null delete
  marker"). rgw-go reads the null version with `getInstanceEntry`, in the
  class's `read_key_entry` order, and when it finds one starts right after
  that version's list key, at the key and NUL ("A versions listing resumes
  after the null version it last visited"), which `encode_list_index_key`
  takes whole as a start key without an instance
  ([cls_rgw.cc:360-363](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L360-L363),
  [cls_rgw.cc:410-413](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L410-L413)). Finding
  no null version, or an unversioned object's entry, it starts past every
  version of the name, after the name, NUL and `w`, where the class starts
  at the name and 0x01 and so leaves out an object named so
  (`docs/ceph-upstream-bugs.md`, "cls_rgw's listing can skip an object
  whose name is another versioned name and 0x01") and any named like the
  name, NUL and more that sorts after rgw-go's start. That costs a versions
  page that starts at `null` one to three `bi_get` reads and, when the
  last finds nothing, one read of the bucket instance past its cache,
  which tells a missing entry from a shard a reshard removed ("An index
  read of a versioned key that finds its shard gone reads the shard the
  bucket now names"). The listing's next marker names `null` for the null
  version and no instance for an unversioned object, and resumes after
  either when copied back. ListObjectVersions passes `version-id-marker` to
  the listing as sent, and writes both as a `NextVersionIdMarker` of `null`,
  as radosgw does
  ([rgw_rest_s3.cc:1817-1825](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L1817-L1825),
  [rgw_rest_s3.cc:1920-1928](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L1920-L1928)),
  so each page after an unversioned object, on a bucket never versioned
  too, starts at `null` and pays those reads.
- **A versions listing resumes after the null version it last visited.**
  radosgw resumes each round of a listing, and each further read of a shard
  in its unordered listing, after the key of the last entry visited
  ([rgw_rados.cc:10130-10133](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10130-L10133),
  [rgw_rados.cc:10856-10859](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10856-L10859);
  [rgw_rados.cc:10303](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10303),
  [rgw_rados.cc:11029](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11029)).
  The null version's key has no instance, which the class takes as the
  name's plain key ([cls_rgw.cc:360-363](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L360-L363),
  [cls_rgw.cc:410-413](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L410-L413)), so a round that
  ends on it lists the name's newer versions, and the null version, again
  into the same page (`docs/ceph-upstream-bugs.md`, "radosgw's versions
  listing repeats a key's newer versions when a round ends on its null
  version"). A round ends short of the page where a truncated shard runs out
  first, as one key's many versions do on a bucket of several shards.
  rgw-go resumes right after the null version's list key, at the key and
  NUL, the next key in byte order, which `bucket_list` takes whole. The
  list key ends in NUL `i`, so a delimiter that ends so
  would make the class take it for a common prefix and start past the
  versions of its epoch
  ([cls_rgw.cc:548-554](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L548-L554),
  [cls_rgw.cc:598-604](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L598-L604);
  `docs/ceph-upstream-bugs.md`, "cls_rgw's start-key delimiter test reads a
  null version's list key as a common prefix"). A delimiter can end the key
  and NUL only in NUL, and the class's jump then lands at the key, NUL and
  0xFF, still before the epoch's versions, so rgw-go's listing keeps them
  where radosgw's own resume from a `version-id-marker` of `null` skips
  them. An unordered listing that resumes there starts on the shard of the
  entry's object name, which rgw-go carries beside the key, as radosgw
  hashes a marker's whole name. A listing without versions
  resumes as radosgw's does, since the class skips the rest of the start
  key's name there ([cls_rgw.cc:610-623](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L610-L623),
  [cls_rgw.cc:660-673](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L660-L673)).
- **A listing sends no repair for the null version's list entry.** A
  listing checks an index entry with a pending op, or one that is not there,
  against its head, and `check_disk_state` suggests the repair under the
  listed entry's own key
  ([rgw_rados.cc:10569-10571](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10569-L10571) and
  [rgw_rados.cc:10663-10667](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L10663-L10667);
  [rgw_rados.cc:11297-11299](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11297-L11299) and
  [rgw_rados.cc:11391-11395](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11391-L11395)).
  `dir_suggest_changes` applies it under `encode_obj_index_key`
  ([cls_rgw.cc:2249-2250](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2249-L2250),
  [cls_rgw.cc:2419-2420](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2419-L2420)), which for the
  null version's list entry is the plain key, on a versioned name the
  version marker. The marker has no pending op and index version 0, so the
  class removes it, or overwrites it with the list entry and adds that entry
  to the bucket's stats
  ([cls_rgw.cc:2309-2374](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L2309-L2374),
  [cls_rgw.cc:2479-2542](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L2479-L2542);
  `docs/ceph-upstream-bugs.md`, "radosgw's listing repair of a null
  version's list entry overwrites or removes the key's version marker").
  rgw-go makes the head check as radosgw does but sends no suggestion for
  such an entry, so it keeps its pending op and the next listing checks it
  again. A named version's repair goes as radosgw sends it and reaches the
  version's instance entry, never its list entry
  (`docs/ceph-upstream-bugs.md`, "radosgw's listing never repairs a
  version's list entry"). rgw-go's own versioned writes leave no list entry
  pending, since each awaits its index completion before its link ("A
  versioned write awaits its index completion").
- **An ordered listing reads the shards the bucket names after a reshard
  removes its own.** radosgw lists through the bucket instance its request
  read, so a shard a reshard removed under the listing answers ENOENT and
  the listing fails with 404 NoSuchKey
  ([rgw_rados.cc:9906-9915](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L9906-L9915),
  [rgw_rados.cc:10631-10639](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10631-L10639)).
  When a shard answers ENOENT, rgw-go's ordered listing reads the bucket
  instance again, past its metadata cache, and when it names other shards
  lists the round again from them, from the key the round started after,
  up to ten times (`reshardedRecord`, `internal/driver/list.go`). The keys
  an ordered listing resumes from name no shard, so its page has no gap or
  repeat. An ENOENT from a shard the bucket still names, or from a bucket
  that is gone, is the listing's answer, 404 NoSuchKey, after one read of
  the bucket instance. The unordered listing's position names a shard of the
  layout it read, so it fails as radosgw's does, and an unordered listing
  paged across a reshard skips and repeats entries as radosgw's does
  (`docs/ceph-upstream-bugs.md`, "radosgw's unordered listing skips and
  repeats entries across a reshard").
- **A system user's listing carries no sync fields.** For a system user's
  request radosgw adds `RgwxTag` to each entry, `VersionedEpoch` and
  `RgwxMtime` to each version, reads `objs-container` and the
  `HTTP_RGWX_SHARD_ID` header, and can list a single index shard
  (`rgw_rest_s3.cc:1724-1737`, `:1838-1845` and `:1947-1949` at v19.2.6,
  `:1835-1848`, `:1949-1956` and `:2058-2060` at v20.2.4). These serve
  multisite sync, which rgw-go excludes, and rgw-go renders a system
  user's listing as any other user's.
- **rgw-go creates no realm, zonegroup or zone, and writes none back.**
  radosgw resolves its zone at startup in `rgw::SiteConfig::load`, which on
  both floors creates a zone and a zonegroup named `default` when `rgw_zone`
  or `rgw_zonegroup` is unset and no realm exists
  (`driver/rados/rgw_zone.cc`, `read_or_create_default_zone` and
  `read_or_create_default_zonegroup`); a Squid radosgw's zone service
  creates them again for an unnamed or explicitly `default` zonegroup and
  zone (`svc_zone.cc:214`, `:240` at v19.2.6); and both releases write a
  zonegroup with one zone and no master back with that zone as master
  (`svc_zone.cc:513-533` and `:595-601` at v19.2.6, `:349-369` and
  `:399-405` at v20.2.4). rgw-go only reads: a missing zone, zonegroup or
  name object stops it at startup with an error naming the object. Rook
  never reaches radosgw's bootstrap: rook v1.20.7 passes `rgw realm`,
  `rgw zonegroup` and `rgw zone` to every gateway
  (`pkg/operator/ceph/object/spec.go:440-442`) and creates the zonegroup
  and zone with `--master` before it starts one (`objectstore.go:470`,
  `:475`; for a zone declared through CephObjectZoneGroup and
  CephObjectZone, `zonegroup/controller.go:257-260` and
  `zone/controller.go:360-363`). A gateway started by hand against a
  cluster without them needs `radosgw-admin realm create`,
  `zonegroup create --master` and `zone create --master` first.
- **rgw-go creates no pool.** radosgw opens its zone's pools with
  `rgw_init_ioctx`'s create flag, so a missing pool is created and tagged
  with the `rgw` application (`driver/rados/rgw_tools.cc:23-98` at
  v19.2.6): the root pool through the configuration store, the domain-root,
  GC, lifecycle, log, reshard and notification pools at startup
  (`RGWRados::init_complete`), and the other metadata, bucket-index and
  data pools on first use. rgw-go never creates one: a missing root,
  control or GC pool stops it at startup with an error naming the pool,
  and a request that needs another missing pool fails with an error naming
  it. Rook creates every pool a store names, or refuses a store whose
  named pools do not exist, before it starts a gateway (rook v1.20.7
  `pkg/operator/ceph/object/controller.go:620-654`,
  `objectstore.go:799-823`).

### Lifecycle differences

rgw-go decides what a lifecycle rule does to a listed object, and what
`x-amz-expiration` and `x-amz-abort-date` name, as radosgw's `rgw_lc.cc`
does at the v19.2.7 and v20.2.4 tags (`lifecycle.Decide`,
`lifecycle.ExpirationHeader` and `lifecycle.AbortHeader`,
`internal/lifecycle/decide.go` and `headers.go`). rgw-go runs the
worker's shard protocol, and its bucket pass applies the current,
delete-marker and noncurrent expiration of any bucket and the abort of its
expired multipart uploads (`internal/driver/lc_bucket.go` and
`lc_actions.go`); it leaves a bucket with a transition rule unclaimed for
radosgw's workers ("rgw-go's lifecycle worker leaves buckets with rules it
does not apply to radosgw's", below). It does not yet send the headers
("No x-amz-expiration", under "Object read differences"). The decision
entries describe what both will do.
Where rgw-go differs, it does the following, as the design's compatibility
rule asks (spec §8, `docs/superpowers/specs/2026-09-25-rgw-go-design.md:284-298`):
the correct S3 result, in RADOS state radosgw reads as its own.

- **A current delete marker at the end of a listing page is kept on
  Squid.** radosgw lists a rule's objects in pages of 1000, and at v19.2.7
  its `next_key_name` answers none for a page's last entry
  ([rgw_lc.cc:434-443](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L434-L443)),
  so a current delete marker there looks to have no older version, and an
  ExpiredObjectDeleteMarker or Expiration rule removes it; its older
  versions start the next page, and the newest becomes current again
  (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle removes a current
  delete marker that ends a listing
  page](ceph-upstream-bugs.md#radosgws-lifecycle-removes-a-current-delete-marker-that-ends-a-listing-page)").
  rgw-go takes v20.2.4's rule on both releases: the last entry of a
  truncated page sees its own name as the next one
  ([rgw_lc.cc:434-451](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L434-L451)),
  so a marker that ends a truncated page is kept whether or not older
  versions follow, and one with none is removed by a pass whose page does
  not end at it (`lifecycle.Lister`). Tentacle and rgw-go agree.
- **A lifecycle pass hands each version of a key out once.** radosgw's
  lister starts each page after the key of the last entry it listed, name
  and instance
  ([rgw_lc.cc:397-412](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L397-L412),
  [rgw_lc.cc:397-412](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L397-L412)),
  and the null version's key has no instance, which the class takes as the
  name's plain key
  ([cls_rgw.cc:360-363](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L360-L363),
  [cls_rgw.cc:410-413](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L410-L413)),
  so a page that ends on a key's null version starts the next at the key's
  first version, and the key's newer versions are listed again: the rules
  act on them a second time, and they count twice toward
  NewerNoncurrentVersions, so a noncurrent version that rule keeps can be
  removed (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle lister lists a
  key's versions again after a page that ends on its null
  version](ceph-upstream-bugs.md#radosgws-lifecycle-lister-lists-a-keys-versions-again-after-a-page-that-ends-on-its-null-version)").
  rgw-go's listing resumes right after the null version's list key while
  that version is there ("A versions listing resumes after the null version
  it last visited"), and its pass drops each entry of the last key it
  handed out that lists at or before the last of them, and passes over an
  entry that lists out of key order before another of its key, as the copy
  of the null version that radosgw's listing repair writes to the key's
  plain entry does (`lcVersionPager`, `internal/driver/lc_bucket.go`). The
  lister takes each entry's next entry from the page as listed, dropped
  entries included, so a current delete marker an older version follows is
  never taken for one no version follows and removed, which would make the
  older version current again (`lcStates`). The lister's state then counts each
  version once, as one listing of the bucket would, and a version written
  since the walk passed its place, the null delete marker the pass itself
  wrote among them, waits for the next pass. radosgw's lister also lists
  its next page without waiting for the work of the last
  ([rgw_lc.cc:1812](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1812),
  [rgw_lc.cc:1868](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1868)),
  and once that work has removed the version a page ends on, the class
  starts the next page past every version of its name, so the rest of the
  key waits for another day (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle lister skips the rest of a key whose version ending a page it
  removed](ceph-upstream-bugs.md#radosgws-lifecycle-lister-skips-the-rest-of-a-key-whose-version-ending-a-page-it-removed)").
  rgw-go lists the next page once the page's work has drained: after the
  newest version of the page's last key that the class still lists as it
  was handed out, or after the one an earlier page resumed after, and only
  when it lists neither at the key's first version
  (`lcVersionPager.resumePoint`, `internal/driver/lc_bucket.go`). A key's
  versions past a page are reached in the same pass, and those the pass
  keeps are not listed again; each version it tries costs one or two index
  reads. A purging removal keeps nothing it lists, so it starts each page
  at its last key's remaining versions ("A purging removal resumes each page
  of a versioned bucket at its last key's remaining versions"); the pass
  keeps most of what it lists, and starting there would list the kept
  versions again, a page for each thousand of them. One wait remains, in
  the state radosgw's listing repair leaves: when the null version's copy
  at the key's plain entry ends a page, the pass hands the copy out, since
  the entry after it is on the next page, and the key's versions newer than
  the null version, which that page lists after the copy, list at or before
  its list key, so they are dropped and wait for the next pass.
- **Lifecycle expiration on a versioning-suspended bucket writes the null
  delete marker.** radosgw's current expiration removes the current
  version outright unless versioning is enabled
  ([rgw_lc.cc:1189](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1189),
  [rgw_lc.cc:1209](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1209)),
  so on a suspended bucket it unlinks that version, null or not, and the
  next newer one becomes current (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle expiration deletes the current version of a
  versioning-suspended
  bucket](ceph-upstream-bugs.md#radosgws-lifecycle-expiration-deletes-the-current-version-of-a-versioning-suspended-bucket)").
  S3 writes a delete marker with the null version ID there, which replaces
  a null current version and keeps any other as noncurrent (the S3 User
  Guide, "Lifecycle configuration elements", the table "Lifecycle actions
  and bucket versioning state"). rgw-go decides that marker
  (`lifecycle.EffectNullMarker`) and writes it (`lcBucket.markCurrent`,
  `internal/driver/lc_actions.go`), the one radosgw's own DeleteObject
  writes on a suspended bucket, so the RADOS state is radosgw's. The marker
  replaces the null version wherever it sits
  ([cls_rgw.cc:1705-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/cls/rgw/cls_rgw.cc#L1705-L1734),
  [cls_rgw.cc:1846-1875](https://github.com/ceph/ceph/blob/v20.2.4/src/cls/rgw/cls_rgw.cc#L1846-L1875)),
  so a noncurrent null version under a current one goes too, as S3's
  suspended DeleteObject removes it, unless object lock keeps it ("Every
  permanent removal by lifecycle runs the object-lock check").
- **Every permanent removal by lifecycle runs the object-lock check.**
  radosgw checks a version's retention and legal hold, through
  `pass_object_lock_check`
  ([rgw_lc.cc:304-349](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L304-L349),
  [rgw_lc.cc:304-349](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L304-L349)),
  only before a noncurrent expiration. On a lock bucket S3 made, versioning
  is enabled and stays so
  ([rgw_op.cc:2788-2793](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L2788-L2793),
  [rgw_op.cc:3020-3025](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3020-L3025)),
  and the current expiration writes a delete marker. But on a bucket whose
  flags carry object lock without `BUCKET_VERSIONED`, or with versioning
  suspended, which a `bucket.instance` metadata put can write and the race
  in "[radosgw's retried PutBucketVersioning skips its object-lock and MFA
  delete
  checks](ceph-upstream-bugs.md#radosgws-retried-putbucketversioning-skips-its-object-lock-and-mfa-delete-checks)"
  can leave, radosgw's current expiration removes a retained or held
  object (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle current
  expiration removes an object under object
  lock](ceph-upstream-bugs.md#radosgws-lifecycle-current-expiration-removes-an-object-under-object-lock)").
  rgw-go runs the same check, `objectlock.VerifyDelete` with no bypass,
  before every removal that deletes a version: a current object of a
  bucket never versioned and a null version the null marker replaces, as
  well as a noncurrent version, and again on the version as read just
  before its removal, on the bucket's record as read then
  (`lcBucket.removeVersion`, `internal/driver/lc_actions.go`). One that
  fails keeps the object, and
  another of the rule's actions may be taken instead, as when a noncurrent
  version fails it. Since the null marker also replaces a noncurrent null
  version, the worker reads the key's null version again before it writes
  the marker over a version that is not null, and a retained or held one
  keeps the marker from being written (`lcBucket.markCurrent`,
  `internal/driver/lc_actions.go`). A delete marker over any other locked
  version is no removal and is written.
- **Lifecycle selection takes an action due at the epoch.** Of a rule's
  actions whose check holds, radosgw takes the one due strictly after the
  latest so far, starting from the epoch
  ([rgw_lc.cc:1623-1634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1623-L1634),
  [rgw_lc.cc:1679-1690](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1679-L1690)),
  so a rule whose Date is 1970-01-01T00:00:00Z, which both gateways'
  PutBucketLifecycleConfiguration take, never acts
  (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle never selects an
  action due at the
  epoch](ceph-upstream-bugs.md#radosgws-lifecycle-never-selects-an-action-due-at-the-epoch)").
  rgw-go takes the first action that holds and then any due strictly
  later, so such a rule acts at once, as for any other date already past.
- **A skipped local midnight is read at the offset now has.** radosgw's
  `round_to_day` returns glibc's `mktime` unchecked
  ([utime.h:218-227](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227),
  [utime.h:218-227](https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L218-L227)),
  which gives -1 for a local midnight a zone skips with the same DST flag
  either side, so `obj_has_expired`'s base is a second before the epoch and
  nothing is due that day (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle finds nothing due on a day whose local midnight a zone skips
  without a DST
  change](ceph-upstream-bugs.md#radosgws-lifecycle-finds-nothing-due-on-a-day-whose-local-midnight-a-zone-skips-without-a-dst-change)").
  rgw-go reads such a midnight at the offset now has, glibc's answer for
  every other skipped midnight, and acts on what is due. Current tzdata
  holds no such day from 2016-10-23 through 2037.
- **A local midnight that occurs twice with the same DST flag is the later
  one.** radosgw's `round_to_day` hands such a midnight to glibc's `mktime`
  ([utime.h:218-227](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227),
  [utime.h:218-227](https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L218-L227)),
  whose answer follows the gateway process's previous `mktime` call:
  Asia/Magadan's on 2014-10-26 is 14:00Z the day before in a fresh process
  and 12:00Z after a call for 2010 (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle finds nothing due on a day whose local midnight a zone skips
  without a DST
  change](ceph-upstream-bugs.md#radosgws-lifecycle-finds-nothing-due-on-a-day-whose-local-midnight-a-zone-skips-without-a-dst-change)").
  rgw-go takes the later, glibc's answer in a fresh process. Current tzdata
  holds no such day from 2023-12-19 through 2037.
- **The lifecycle size filter reads a version's own, uncompressed size.**
  radosgw's `pass_size_limit_checks` stats the listed key with the OLH
  followed, so a noncurrent null version is judged by the current
  version's size, and compares the size stored, which for a compressed
  object is its compressed size
  ([rgw_lc.cc:516-545](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L516-L545),
  [rgw_lc.cc:524-552](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L524-L552);
  docs/ceph-upstream-bugs.md, "[radosgw's lifecycle size filter reads the
  wrong size](ceph-upstream-bugs.md#radosgws-lifecycle-size-filter-reads-the-wrong-size)").
  S3's ObjectSizeGreaterThan and ObjectSizeLessThan are the object's size.
  rgw-go reads the listed version by its own key, `null` for a null
  version, and its uncompressed size (`lifecycle.Probe`'s `Size`). It reads
  every delete marker as size 0, as radosgw's stat of a marker with an
  instance gives; radosgw's stat of a null marker, listed without one,
  follows the OLH, so its filter fails for a current null marker and reads
  the current version's size for a noncurrent one. A size-filtered rule
  therefore treats a null marker as it treats any other marker.
- **The lifecycle tag filter needs each of a rule's tags among the
  object's.** radosgw's worker, in `has_all_tags`
  ([rgw_lc.cc:1030-1050](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1030-L1050),
  [rgw_lc.cc:1050-1070](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1050-L1070)),
  counts the object's tags whose value matches the rule's for their key,
  so a tag set that repeats a key can stand in for a tag it lacks, and
  `s3_expiration_header` reads only the first value of a repeated key
  ([rgw_lc.cc:2830-2848](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2830-L2848),
  [rgw_lc.cc:2893-2911](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2893-L2911);
  docs/ceph-upstream-bugs.md, "[radosgw's lifecycle tag filter counts an
  object's repeated tag key toward the rule's other
  tags](ceph-upstream-bugs.md#radosgws-lifecycle-tag-filter-counts-an-objects-repeated-tag-key-toward-the-rules-other-tags)").
  rgw-go takes a rule for an object, in the worker and the header alike,
  only when each tag of the rule is among the object's, its key with its
  value. The two agree on every tag set without a repeated key, which S3
  refuses.
- **x-amz-expiration names the Expiration rule alone on Tentacle.**
  v20.2.4's `s3_expiration_header` gives a request that names a version a
  header, and for a key with an instance, which a GET or HEAD of a current
  version with a version ID also passes, takes the date from the rule's
  NoncurrentVersionExpiration
  ([rgw_lc.cc:2817-2952](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2817-L2952);
  docs/ceph-upstream-bugs.md, "[radosgw's x-amz-expiration reports a
  NoncurrentVersionExpiration
  rule](ceph-upstream-bugs.md#radosgws-x-amz-expiration-reports-a-noncurrentversionexpiration-rule)").
  rgw-go computes v19.2.7's form on both releases
  ([rgw_lc.cc:2750-2888](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2750-L2888)):
  the Expiration rule alone, and no header for a request that names a
  version. Squid and rgw-go agree.
- **Lifecycle header dates past 24855 days or real_time are not wrapped.**
  radosgw counts DaysAfterInitiation in `int` seconds, which overflow past
  24855 days, and an Expiration's Days in 64-bit nanoseconds, which
  overflow past about 106751, and either date wraps past real_time's last
  instant in 2554
  ([rgw_lc.cc:2932-2935](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2932-L2935),
  [rgw_lc.cc:2996-2999](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2996-L2999);
  [rgw_lc.cc:2859-2863](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2859-L2863),
  [rgw_lc.cc:2923-2927](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2923-L2927);
  docs/ceph-upstream-bugs.md, "[radosgw's lifecycle headers wrap a date
  past 24855 days or
  real_time](ceph-upstream-bugs.md#radosgws-lifecycle-headers-wrap-a-date-past-24855-days-or-real_time)").
  rgw-go counts in 64-bit seconds and names the date the days give. Rules
  of fewer days, all a client is likely to send, get the same date from
  both.
- **Lifecycle headers name the midnight the worker acts at.** radosgw's
  x-amz-expiration and x-amz-abort-date take the midnight UTC that begins
  the mtime's day, plus the days and one more
  ([rgw_lc.cc:2859-2863](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2859-L2863),
  [rgw_lc.cc:2923-2927](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2923-L2927);
  [rgw_lc.cc:2932-2935](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2932-L2935),
  [rgw_lc.cc:2996-2999](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2996-L2999)),
  while its worker acts once midnight is the days past the mtime's whole
  second (`obj_has_expired`,
  [rgw_lc.cc:272-302](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L272-L302),
  [rgw_lc.cc:272-302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L272-L302)).
  For an object written in a day's first second the headers name the day
  after the one the worker acts on (docs/ceph-upstream-bugs.md,
  "[radosgw's lifecycle headers name the day after the worker acts for an
  object written in a day's first
  second](ceph-upstream-bugs.md#radosgws-lifecycle-headers-name-the-day-after-the-worker-acts-for-an-object-written-in-a-days-first-second)").
  rgw-go names the first midnight UTC at least the days past the mtime's
  second, the one a worker in UTC acts at, so for that object it names a
  day earlier than radosgw. Every other mtime gets radosgw's date.
- **The lifecycle work window must scan as four numbers.** With
  `rgw_enable_lc_threads` on, rgw-go refuses to start on an
  `rgw_lifecycle_work_time` that does not hold four numbers of `int` in the
  form `%d:%d-%d:%d`, naming the option and the value: one of fewer
  numbers, such as `01:00-06`, a space before the dash, as in
  `01:00 - 06:00`, or a number beyond `int`. radosgw starts, and its worker
  compares the window with numbers the scan never set
  ([rgw_lc.cc:2510-2570](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2510-L2570),
  [rgw_lc.cc:2577-2637](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2577-L2637);
  docs/ceph-upstream-bugs.md, "[radosgw's lifecycle work window reads
  numbers rgw_lifecycle_work_time does not
  give](ceph-upstream-bugs.md#radosgws-lifecycle-work-window-reads-numbers-rgw_lifecycle_work_time-does-not-give)").
  A value that scans is read as radosgw's `sscanf` reads it: white space
  before a number skipped, a sign taken, text after the fourth number
  ignored, and hours and minutes outside a day kept. With the threads off
  rgw-go does not scan the option, and radosgw, which reads it only in its
  lifecycle threads, does not read it
  ([rgw_rados.cc:1368-1369](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L1368-L1369),
  [rgw_rados.cc:1423-1424](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L1423-L1424)).
- **Lifecycle worker counts that are not positive.** With
  `rgw_enable_lc_threads` on, rgw-go uses a negative
  `rgw_lc_max_worker` as 0, and runs no lifecycle worker, and an
  `rgw_lc_max_wp_worker` that is not positive as 1, each with an
  error-level log line naming the option and the value read. radosgw
  throws at startup on the negative worker count, and narrows the workpool
  size to 16 bits: a size that narrows to 0 divides by zero at the first
  lifecycle work item, and another negative size runs its remainder modulo
  65536 of threads in each worker's pool
  ([rgw_lc.cc:2467-2477](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2467-L2477),
  [rgw_lc.cc:832-879](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L832-L879);
  [rgw_lc.cc:2534-2544](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2534-L2544),
  [rgw_lc.cc:853-900](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L853-L900);
  docs/ceph-upstream-bugs.md, "[radosgw aborts on a negative
  rgw_lc_max_worker and divides by zero on an rgw_lc_max_wp_worker of
  0](ceph-upstream-bugs.md#radosgw-aborts-on-a-negative-rgw_lc_max_worker-and-divides-by-zero-on-an-rgw_lc_max_wp_worker-of-0)").
  rgw-go uses a workpool size above 65535 as given, where radosgw narrows
  it. With the threads off neither gateway uses either count, and rgw-go
  logs nothing about them.
- **A lifecycle entry's write releases only a shard lock it took.** A
  lifecycle configuration put or delete writes the bucket's entry under
  its shard's `lc_process` lock, as radosgw's `guard_lc_modify` does,
  retrying a locked shard every 100 ms up to 500 times
  ([rgw_lc.cc:2584-2634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2584-L2634),
  [rgw_lc.cc:2651-2701](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2651-L2701)).
  When it cannot take the lock, radosgw unlocks the shard all the same,
  which releases the gateway's own hold on the shard when one holds it,
  another request's or its worker's `bucket_lc_post`
  (docs/ceph-upstream-bugs.md, "[radosgw's guard_lc_modify unlocks a
  lifecycle shard lock it did not
  take](ceph-upstream-bugs.md#radosgws-guard_lc_modify-unlocks-a-lifecycle-shard-lock-it-did-not-take)").
  rgw-go unlocks only a lock it took, and fails with the lock's error.
- **rgw-go's lifecycle worker departs from radosgw's shard protocol in
  three places.** It takes the shard's `lc_process` lock under worker
  ix's cookie, `lc_thrd: <ix>`, with radosgw's five tries, and keeps the head, the
  claim, the post-state, the skips, the session expiry and the rollover
  as `RGWLC::process` does ([rgw_lc.cc:2252-2465](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2252-L2465),
  [rgw_lc.cc:2317-2532](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2317-L2532); `internal/driver/lc_worker.go`), so radosgw's
  workers and rgw-go's can share a zone. That includes the rollover
  check's gap, which lets every pass within a day of a shard's rollover
  walk it again (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle
  rollover guard does not keep a second pass from walking a
  shard](ceph-upstream-bugs.md#radosgws-lifecycle-rollover-guard-does-not-keep-a-second-pass-from-walking-a-shard)").
  It departs:
  - An entry its bucket pass may not claim keeps the shard's head. rgw-go
    neither claims it nor moves the head past it, and walks on with a
    marker of its own, from that entry to the shard's end and then from
    the shard's first entry back to it, claiming each due entry its bucket
    pass may claim and writing no head. Leaving the head there lets radosgw's worker reach the
    entry, and the walk reaches the entries before it, which radosgw's
    walk reaches only once its head does. A pass that leaves such an entry
    stamps no rollover date, since its head never reaches the shard's end,
    and in a zone without radosgw workers the entry is never processed.
    Such an entry is a bucket whose rules transition to a cloud tier, and
    one with a transition rule (the next two entries).
  - A claim is read back before its bucket is processed, before each
    listing page of the bucket's pass, and again once the shard is locked
    after it. A claim the shard no longer holds as the worker wrote it,
    because another worker took over its expired session, a lifecycle
    configuration put reset it, or a bucket delete removed it, gets no
    post-state, and the worker's pass on that shard ends there without
    writing the head. radosgw writes its post-state over whatever
    the entry then holds, which overwrites the other worker's claim,
    completes a reset entry for the day, or puts a removed entry back, and
    when the head it advanced before the bucket had reached the shard's
    end, writes that head back with a rollover date
    (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle post-state
    overwrites an entry changed during the bucket's
    pass](ceph-upstream-bugs.md#radosgws-lifecycle-post-state-overwrites-an-entry-changed-during-the-buckets-pass)").
    A claim lost during the bucket's pass ends that pass at its next page,
    where radosgw goes on with the bucket to its end. The same check stops
    the pass at `thread_stop_at`, a lifecycle day after it began, which
    radosgw checks at each rule and every 100 entries
    ([rgw_lc.cc:1770-1775](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1770-L1775)
    and [rgw_lc.cc:1816-1822](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1816-L1822),
    [rgw_lc.cc:1826-1831](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1826-L1831)
    and [rgw_lc.cc:1872-1878](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1872-L1878)),
    so rgw-go's pass can hand out up to about 900 entries more past its
    stop than radosgw's (`lcBucket.atPage`, `internal/driver/lc_bucket.go`).
  - A worker that stops while it processes a bucket, as at a gateway's
    shutdown, or between a claim and its first read back, records the
    bucket failed. On a context of its own, bounded at 5 s, it takes the
    shard's lock again with the backoff, reads its claim back, and writes
    `failed` under the claim's start time, so the bucket is due again at
    the next pass that reaches it; it then releases the lock on a second
    bound of 5 s, so a slow cluster that used up the first still gets the
    unlock (`lcShard.recordStop`, `internal/driver/lc_worker.go`). A pass
    that returned on its own records its result on the same kind of
    context, so a stop that lands during its post-state does not cut it
    short. The worker sends the lock and the unlock as write ops, which
    goceph's default callback completion mode and its pipe mode stop
    waiting for at a bound, so in those modes both bounds hold whatever the
    cluster does. goceph's sync mode, `--rados-completions=sync`, waits for
    the OSD, as go-ceph's lock calls do, and in every mode the RADOS
    client's close still waits for an op left in flight, so a lock or
    unlock on an unavailable PG still holds the gateway's exit
    (docs/cgo-limitations.md, "Cancellation stops only the client"). Once
    the worker's context has ended, its unlock between buckets and before a
    bucket's pass is bounded at 5 s too. A step that fails, or a bound
    running out, records nothing and leaves the entry processing, as a
    gateway that dies leaves it, and a lock or unlock left in flight leaves
    the shard locked until `rgw_lc_lock_max_time`, 90 s by default, lapses
    it, while radosgw's `guard_lc_modify` gives up on a locked shard after
    50 s and fails the lifecycle configuration put or delete
    ([rgw_lc.cc:2584-2634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2584-L2634), [rgw_lc.cc:2651-2701](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2651-L2701)). radosgw records nothing:
    once it is told to stop, its work queues stop, the bucket pass's next
    drain of its work pool never returns, and the process is killed after
    `rgw_exit_timeout_secs` or the orchestrator's grace period with the
    entry processing, so every worker skips the bucket until its session
    expires two lifecycle days on. A radosgw worker between buckets first
    claims one more (docs/ceph-upstream-bugs.md, "[radosgw's lifecycle worker hangs shutdown in WorkPool::drain and claims a bucket after it is told to stop](ceph-upstream-bugs.md#radosgws-lifecycle-worker-hangs-shutdown-in-workpooldrain-and-claims-a-bucket-after-it-is-told-to-stop)").
- **An rgw-go-only zone never processes a bucket whose rules transition to
  a cloud tier.** radosgw's worker transitions an object to a storage
  class that the bucket's placement target tiers to a cloud, a tier of
  type `cloud-s3` at v19.2.7 and `cloud-s3` or `cloud-s3-glacier` at
  v20.2.4
  ([rgw_lc.cc:1479-1488](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1479-L1488),
  [rgw_lc.cc:1525-1534](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1525-L1534);
  [rgw_zone_types.h:659-662](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_zone_types.h#L659-L662)),
  which rgw-go does not serve ("Cloud transition and restore"). rgw-go
  never claims a bucket with a rule the zone runs that transitions, current
  or noncurrent versions, to a tier of either type, on either release, and
  leaves the shard's head at its entry, so radosgw's worker processes the
  bucket whole, its other rules included (`lcPass.refusal`,
  `internal/driver/lc_bucket.go`). In a zone of rgw-go gateways alone none
  of that bucket's rules run.
- **rgw-go's lifecycle worker leaves buckets with rules it does not apply to
  radosgw's.** rgw-go's bucket pass applies the current expiration, by a
  delete marker on a bucket whose versioning is enabled and the null delete
  marker on a suspended one, the removal of expired delete markers and of
  expired noncurrent versions, and the abort of expired multipart uploads
  ("Lifecycle aborts an expired multipart upload under its completion
  lock"). It claims a bucket only when it applies each of the bucket's
  rules, so not one with a rule the zone runs that transitions current or
  noncurrent versions. A
  claim marks a bucket done for the day, and radosgw's worker would then
  skip the rules rgw-go does not apply. Such a bucket's entry is left
  unclaimed, which is logged once a pass, and the shard's head stays at it,
  so radosgw's worker reaches it ("rgw-go's lifecycle worker departs from
  radosgw's shard protocol in three places"). A bucket that becomes one
  between its claim and its pass is recorded failed, so either worker takes
  it again at its next pass. In a zone of rgw-go gateways alone such a
  bucket's rules never run, and every other bucket of its shard is
  processed on every pass. radosgw's worker claims every bucket and applies
  each of its rules
  ([rgw_lc.cc:1679-1830](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1679-L1830),
  [rgw_lc.cc:1735-1886](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1735-L1886)).
- **Lifecycle removes an object only as it was listed.** radosgw's
  `remove_expired_obj` guards a current expiration's removal with the
  listed mtime alone, as `unmod_since`
  ([rgw_lc.cc:643](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L643),
  [rgw_lc.cc:662](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L662)),
  which the unversioned delete compares in whole seconds
  ([rgw_rados.cc:5995-6010](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5995-L6010),
  [rgw_rados.cc:6609-6624](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6609-L6624)),
  so it removes an object written again within that second, and one whose
  attrs changed after its checks, a tag set that no longer matches the
  rule's filter among them (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle expiration guards its removal with the listed mtime's second
  alone](ceph-upstream-bugs.md#radosgws-lifecycle-expiration-guards-its-removal-with-the-listed-mtimes-second-alone)").
  rgw-go reads the object again first and goes on only while it is the one
  listed, at the listed mtime to the nanosecond and with the listed ETag,
  and, when its decision read the object for a tag, size or lock check, at
  the RADOS version that read returned. It checks object lock on what it
  read again, and removes it with the listed mtime and ETag as conditions
  and the RADOS version its read again returned asserted in the removal's
  op (`lcBucket.expireCurrent`, `internal/driver/lc_actions.go`;
  `DeleteParams.AssertEpoch`, `internal/op/object.go`). A write to the
  head after a read the removal rests on, an attr write that keeps the
  mtime and the ETag included, so leaves the object and its index entry,
  and the object is decided afresh at the next pass.

  On a bucket that was ever versioned the worker acts on the version listed
  in the same way (`lcBucket.markCurrent` and `lcBucket.removeVersion`,
  `internal/driver/lc_actions.go`):
  - The current expiration writes its delete marker only while the key's
    current version, read again through its OLH, is the instance listed, at
    the listed mtime and ETag, and the marker carries that ETag and mtime as
    If-Match and `x-amz-if-match-last-modified-time`, which the delete
    checks on the version current when the marker links, atomically on an
    enabled bucket ("A conditional delete marker is checked against the
    version current when it links"). radosgw's marker carries `unmod_since`
    alone, which the class compares only with an instance entry that exists,
    and an enabled bucket's marker has a fresh instance, so it can hide a
    version a PUT wrote after the listing (docs/ceph-upstream-bugs.md,
    "[radosgw's lifecycle writes a delete marker over a version put after
    its
    listing](ceph-upstream-bugs.md#radosgws-lifecycle-writes-a-delete-marker-over-a-version-put-after-its-listing)").
    On an enabled bucket the marker also carries the version as read again
    as `DeleteParams.Expect`, which the delete requires current, by its
    instance and its write tag, when its condition reads the version
    current, a read the link's check holds to the link as the marker's
    other conditions are, so a version a PUT wrote after the read again,
    whatever its body and mtime, and an attr write before that read both
    keep the marker off. The check holds the read against links alone, so
    an attr write landing between the condition's read and the link goes
    unseen; the marker over it removes nothing on an enabled bucket, and the
    one effect is a marker over a version whose attrs, its tags among them,
    changed in that window. The null version is judged by its
    index entry, read again, and its marker carries no
    `x-amz-if-match-last-modified-time`, which the delete would check on the
    key's OLH head: that head holds the null version's data, and a write to
    it that sets no mtime, as radosgw's removal of a modification's pending
    attr, moves its mtime (docs/ceph-upstream-bugs.md, "[radosgw's removal of
    an OLH head's pending attr moves the null version's
    mtime](ceph-upstream-bugs.md#radosgws-removal-of-an-olh-heads-pending-attr-moves-the-null-versions-mtime)");
    its `Expect`, the OLH head's write tag, guards it instead.
  - On a suspended bucket the null delete marker carries the key's OLH head
    as read again as `DeleteParams.Expect`, whose write tag is the null
    version's, or, on a key with no null data, the one the key's first OLH
    init gave it, so a null write or an attr write since refuses the marker
    ("A null delete marker can be conditional on the null version it
    replaces"). It carries no `unmod_since`: the class compares that with the
    null version's own entry and, finding it no older, would write nothing.
  - A delete marker is removed by its version only while its index entry is
    a marker at the listed versioned_epoch, which tells one marker from
    another, and its removal carries the marker as read again, its
    versioned_epoch and mtime, as `Expect` and that mtime as
    `x-amz-if-match-last-modified-time`. The listed mtime is not compared: a
    null delete marker takes the pending op of the null entry it replaces,
    which a slow index completion leaves there, and a listing reports a
    marker that carries one with the OLH head's mtime
    (docs/ceph-upstream-bugs.md, "[radosgw's listing repair of a null
    version's list entry overwrites or removes the key's version
    marker](ceph-upstream-bugs.md#radosgws-listing-repair-of-a-null-versions-list-entry-overwrites-or-removes-the-keys-version-marker)"),
    so such a marker would never match its listing and never expire.
  - A noncurrent version is removed by its instance, `null` for an empty
    one, only while it is still the version listed, the null version judged
    by its index entry. The removal carries the listed ETag and mtime as
    If-Match and `x-amz-if-match-last-modified-time`, which the delete
    checks on the version's index entry, and the version as read again as
    `Expect`, which the delete checks before its unlink and again in the
    replay that removes the version's head. So a null version that a
    suspended bucket's PUT rewrote since is kept, where radosgw's removal of
    a named version takes no condition (docs/ceph-upstream-bugs.md,
    "[radosgw's versioned delete of a named version ignores unmod_since,
    which its lifecycle relies
    on](ceph-upstream-bugs.md#radosgws-versioned-delete-of-a-named-version-ignores-unmod_since-which-its-lifecycle-relies-on)").
    A removal the replay refuses lists the version again at its own epoch
    ("A refused versioned removal re-links the version, unless a newer null
    write owns the null key"), and the next pass decides it afresh. The
    window that stays is radosgw's own: another reader's replay of the
    unlink's log entry still removes the version.
- **Lifecycle stops a bucket's pass when the bucket changes under it.**
  radosgw's `bucket_lc_process` loads the bucket once and acts on that
  record until its pass ends, up to a lifecycle day later
  ([rgw_lc.cc:1697-1703](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1697-L1703),
  [rgw_lc.cc:1753-1759](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1753-L1759)),
  so a bucket whose versioning is enabled during the pass has its expired
  objects removed outright, through the unversioned delete, where a delete
  marker would keep each as a noncurrent version (docs/ceph-upstream-bugs.md,
  "[radosgw's lifecycle pass acts on the bucket record it loaded at its
  start](ceph-upstream-bugs.md#radosgws-lifecycle-pass-acts-on-the-bucket-record-it-loaded-at-its-start)").
  rgw-go reads the bucket's record again before each listing page, and
  again just before each removal, and acts with that record
  (`lcBucket.atPage` and `lcBucket.current`, `internal/driver/lc_bucket.go`):
  a bucket the claim predicate now refuses, its rules transitioning, one
  whose versioning was enabled or suspended since the pass began, or whose
  lifecycle configuration is no longer the one the pass began with, ends
  the pass, recorded failed, so either worker takes it again at its next
  pass, and the removal or marker that found it so is not made; a bucket
  that no longer loads under the entry's marker ends it and the entry is
  removed. The window that remains is one
  removal's: from that last read to the removal's own op, which holds the
  removal's head read and its index prepare, and with the metadata cache
  on, also a change whose cache notify has not yet reached the gateway. A
  notify that never arrives, which the writing gateway logs and goes on
  from
  ([svc_sys_obj_cache.cc:346-348](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_sys_obj_cache.cc#L346-L348),
  [svc_sys_obj_cache.cc:346-348](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_sys_obj_cache.cc#L346-L348)),
  leaves the cached record stale until its entry expires,
  `rgw_cache_expiry_interval` after it was cached, 900 s by default
  ([rgw.yaml.in:3330-3342](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3330-L3342),
  [rgw.yaml.in:3509-3521](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3509-L3521)),
  or until a control watch breaks, which flushes the cache; rgw-go's cache
  takes that option with that default (`objectCache`,
  `internal/driver/cache.go`). An object whose bucket's versioning is
  enabled in the window is still removed outright, as on radosgw. The read
  before a removal goes through the metadata cache, so with
  `rgw_cache_enabled` off each removal costs a RADOS read of the bucket's
  entry point and one of its instance.
- **Lifecycle aborts an expired multipart upload under its completion
  lock.** radosgw's worker aborts an upload whose meta object is
  DaysAfterInitiation old with the upload's abort alone
  ([rgw_lc.cc:902-947](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L902-L947),
  [rgw_lc.cc:923-967](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L923-L967)),
  without the `RGWCompleteMultipart` lock AbortMultipartUpload takes first
  ([rgw_op.cc:6643-6655](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6643-L6655),
  [rgw_op.cc:7560-7572](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7560-L7572)),
  so it can free the parts of a completion in progress. rgw-go aborts it as
  AbortMultipartUpload does, its upload read and then its abort under the
  lock (`lcBucket.abortUpload`, `internal/driver/lc_actions.go`), with that
  abort's differences ("AbortMultipartUpload removes the upload before it
  frees the parts" and "AbortMultipartUpload leaves a completed upload's
  parts alone"): an upload whose lock another gateway holds is logged and
  left for a later pass, and one whose parts back its key's object keeps
  them. Neither applies the rule's tag or size filter or the ArchiveZone
  check to an upload, and both run the abort for every enabled rule with
  DaysAfterInitiation, on any bucket rgw-go claims ("rgw-go's lifecycle
  worker leaves buckets with rules it does not apply to radosgw's").
- **Lifecycle current expiration never acts on a stored Date past
  real_time.** radosgw's worker reads a stored Expiration Date past
  2554-07-21T23:34:33.709551615Z as the instant real_time wraps it to and
  expires once that is past
  ([rgw_lc.cc:1145-1154](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L1145-L1154),
  [rgw_lc.cc:1165-1174](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L1165-L1174)),
  so a rule whose Date is `2600-01-01T23:34:33.709551616Z`, which its
  PutBucketLifecycleConfiguration stores, expires every object it matches
  at once, as of 2015-06-14 (docs/ceph-upstream-bugs.md, "[radosgw's
  lifecycle Date wraps past 2554 and expires at
  once](ceph-upstream-bugs.md#radosgws-lifecycle-date-wraps-past-2554-and-expires-at-once)").
  rgw-go's prefix map holds no date for it ("The lifecycle prefix map
  reads a stored negative object size by its sign, and no Date past
  real_time"), so the rule's Expiration acts on no object and its objects
  are kept.
- **A lifecycle entry stored in its pair form is rewritten.** A shard
  entry stored as the pair of bucket and status, as releases through
  v15.2.4 stored it, stops radosgw's worker on that shard, since
  `lc_get_entry` and `lc_get_next_entry` answer EIO for it
  (docs/ceph-upstream-bugs.md, "[cls_rgw's lc_get_entry and
  lc_get_next_entry refuse an lc entry stored in its old pair form, which
  stalls the
  shard](ceph-upstream-bugs.md#cls_rgws-lc_get_entry-and-lc_get_next_entry-refuse-an-lc-entry-stored-in-its-old-pair-form-which-stalls-the-shard)").
  On that EIO rgw-go's worker reads the key's value from the shard's
  omap, the head's key or the first key after the marker, as
  `lc_list_entries` reads a value, an entry else the pair; it rewrites it
  under the shard's lock as the entry {bucket, 0, status}, the form
  radosgw stores from v15.2.5 and its worker reads, and goes on
  (`lcShard.headEntry` and `lcShard.nextEntry`,
  `internal/driver/lc_worker.go`). A value that is neither form, or a pair
  naming another key, is left as it is and ends the pass on that shard
  with the head where it was, as radosgw's stall leaves it.
- **Lifecycle days and the work window are read in the gateway's local
  time zone.** radosgw reads whether a bucket ran today, an object's age
  and `rgw_lifecycle_work_time` in the process's local time, through glibc's
  `localtime_r` and `mktime` ([rgw_lc.cc:2149-2171](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2149-L2171) and
  [rgw_lc.cc:2510-2570](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2510-L2570), [rgw_lc.cc:2213-2235](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2213-L2235) and
  [rgw_lc.cc:2577-2637](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2577-L2637);
  [utime.h:218-227](https://github.com/ceph/ceph/blob/v19.2.7/src/include/utime.h#L218-L227),
  [utime.h:218-227](https://github.com/ceph/ceph/blob/v20.2.4/src/include/utime.h#L218-L227)),
  and rgw-go reads them in Go's `time.Local`, which takes the zone from
  the same places: `TZ` naming a zone file, `/etc/localtime` when `TZ` is
  unset, and UTC when it is empty (`time/zoneinfo_unix.go:28-80` at
  go1.27.3). A `TZ` that holds a POSIX rule rather than a zone's name,
  such as `XYZ-3`, is that rule to
  glibc and UTC to rgw-go, so such a deployment names a zone file instead.
  Each gateway processes a bucket once per its own local day, so the
  gateways of a zone must share a time zone, or a bucket is processed
  once per day of each: duplicate passes, not corruption. Rook sets no `TZ`
  on its gateways (rook v1.20.7 names none under `pkg/`).

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
index-entry methods back resharding and index repair, and the S3
versioning path needs one of them, `bi_get`: radosgw's versioned delete
reads one version's index entry with it, and `repair_olh` a name's OLH
entry. rgw-go reads those, and one version's entries for its own lookups,
up to three reads for `null` in the class's `read_key_entry` order;
`bi_put`, `bi_list` and the repair paths are out on both sides. Feature
level: both decode class replies up to what main writes. For driver-level
types both gateways
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
