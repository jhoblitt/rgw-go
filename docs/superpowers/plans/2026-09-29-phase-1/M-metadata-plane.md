# Phase 1 Unit M: Metadata Plane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Status:** complete (2026-09-28); every task is written and the self-review is done.

**Goal:** Fill the first half of the RADOS driver so the gateway serves buckets: zone, zonegroup, realm and period resolved from the root pool exactly as radosgw resolves them under Rook, placement rules resolved to pools and namespaces, radosgw's metadata cache kept coherent with coexisting gateways through the `notify.N` control objects in both directions, users read through their index objects, bucket entry points and instances read and written under cls_version, the per-owner bucket list through cls_user, CreateBucket, DeleteBucket, HeadBucket, GetBucketLocation and ListBuckets, ListObjects v1 and v2 with radosgw's pending-entry reconciliation, quota enforcement over radosgw's stats caches, the usage-log accumulator and its flush worker, and the bucket ACL, policy and tagging subresources.

**Architecture:** `internal/driver` grows from G's skeleton into the metadata plane in layers that mirror radosgw's services: `sysobj` (a versioned read/write of one RADOS object composed the way `RGWSI_SysObj_Core` composes it), `cache` (an LRU with radosgw's flags, negative entries and expiry, disabled whenever a control watch is down), `notify` (the eight control objects, watch re-registration with backoff, and the cache-notify record sent after every metadata write), `zone` (resolution and placement), `user`, `bucket`, `list`, `stats`, `usage`, each a file or two. `internal/op` gains the bucket ops and `internal/s3` the bucket handlers in M's own file, `bucket.go`, registered through G's `bucketHandlers()`. Every RADOS interaction goes through the phase-0 seam and class packages; nothing here imports go-ceph. Every claim about radosgw below was verified at ceph v19.2.6 and v20.2.4 (`git show <tag>:<path>`); each task cites its sources inline.

**Tech Stack:** Go 1.27, the phase-0 packages (`denc`, `meta`, `acl`, `radosclient`, `cls/version`, `cls/user`, `cls/rgw`), G's `op`, `cephconf`, `s3`, `memstore` and `driver` skeleton, Z's `acl`, `tags`, `policy` and `authz`, log/slog, golang.org/x/sync (errgroup, semaphore), Ginkgo v2 and Gomega, counterfeiter fakes (`internal/op/opfakes`, `internal/radosclient/radosclientfakes`), rooket disposable clusters (`hack/rooket/`) for the `[cluster]` tasks.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md` (§8 encoding and coexistence, §9 phase 1), read with the index's background in `docs/superpowers/plans/2026-09-29-phase-1/00-index.md` (unit M in "The nine units", "Shared interfaces and seam gaps", decisions D4 and D5 in "Spec ambiguities and decisions D1-D11"), G's plan `docs/superpowers/plans/2026-09-29-phase-1/G-gateway-core.md` ("Frozen interface contract", normative), Z's plan `docs/superpowers/plans/2026-09-29-phase-1/Z-authorization.md` ("Frozen interface contract additions", normative), W's plan for the additive `StatsStore.AdjustStats` (W-D7), `docs/exclusions.md` (coexistence obligations) and `docs/ceph-upstream-bugs.md` (the entries baked in below).

## Global Constraints

- Module `github.com/jhoblitt/rgw-go`; `go 1.27` in `go.mod`, minor only, no `toolchain` line; every `go` command carries `-tags=ceph_preview` (the Makefile's `GO_TAGS`).
- Layout per the Go canon: everything under `internal/`; package names one lowercase word; no `util`, `common` or `helpers`. `driver` imports `op`, `meta`, `acl`, `cls/*`, `radosclient`, `cephconf`, `denc` and nothing above them; `s3` and `op` never import `driver`; `radosclient/goceph` stays the only package importing go-ceph.
- G's frozen interface contract is built against verbatim: names and signatures in `op`, `s3`, `memstore`, `driver` and `cephconf` are not renamed or removed. The additive changes this plan makes (`op.ListObjectsParams.AllowUnordered`, `op.StatsStore.AdjustStats` shared with W, `op.UsageEntry.Payer`, `op.AttrIAMPolicy`/`op.AttrPublicAccess`, `op.LogUsage`, `op.RetryRacedBucketWrite`, `s3.objectGetACLs`/`s3.objectPutACLs`, `meta.ObjVersion` when G omitted it, `meta.StrHashLinux`, `meta.ParseIndexKeyName`) regenerate the fakes and extend `memstore`; `make generate-check` fails a stale fake.
- Z's contract additions are consumed as written: `acl.DefaultPolicy`, `acl.Canned`, `acl.FromHeaders`, `acl.ParseS3XML`, `acl.Policy.MarshalS3XML`, `tags.ParseXML`, `tags.Decode`, `tags.Set.MarshalXML`, `authz.Evaluator.BuildACL`/`BuildDefaultACL`/`ParseBucketPolicy`, `authz.UserResolver`, `authz.ErrorFor`.
- Tests are Ginkgo v2 and Gomega only, one `<pkg>_suite_test.go` per package with `RandomizeAllSpecs` and `FailOnPending`; fakes are built in `BeforeEach`; asynchronous behaviour is observed with `Eventually`/`Consistently` carrying explicit timeouts or an injected clock, never `time.Sleep`; integration specs carry `//go:build integration` and `Label("integration")` and read the cluster from `RGW_GO_TEST_CEPH_CONF` through `internal/testutil/cephtest`.
- Logging is `log/slog`, JSON to stderr, static lowercase messages, snake_case attribute keys, the `…Context` variants wherever a `ctx` is in scope; never a secret (access keys, secret keys) at any level.
- Errors wrap with `%w`, lowercase, unpunctuated; `errors.Is`/`errors.As` only; every goroutine has a stop condition; fan-out is `errgroup.WithContext`; a worker registered with `driver.Store.AddWorker` returns when its context ends.
- Encoding rule (spec §8): persistent types decode every version and encode at `s.release`; class requests encode at `s.release`; the release is G's `Open` detection or its override and is never re-detected.
- Ceph config is the single source of every tunable; each `rgw_*` option is read once at `Open` through `cephconf.Options` and its default is radosgw's (`rgw.yaml.in` at v19.2.6, listed per task). No new rgw-go flag or environment variable is introduced.
- rgw-go never creates a pool (decision D4), never creates a realm, zonegroup or zone and never writes radosgw's master-zone fix-up (M-D1), all of which radosgw does: a missing zone object, root pool or control pool is a startup error naming it, and a missing pool opened later (`poolCache.get`) is an error naming it on the request that needs it, never a bootstrap. Task 13 records both differences in `docs/exclusions.md`.
- Control-pool notifies are never applied as truth: a notify of either op invalidates the key and the next read re-reads RADOS (`docs/ceph-upstream-bugs.md`, "radosgw caches any control-pool UPDATE_OBJ notify payload unchecked"). The gateway's cephx caps on the control pool are the narrow ones Rook grants; nothing here widens them.
- Control-watch re-registration backs off between attempts (1 s doubling to 30 s), retries for ever, resets on success and never counts failures over the process lifetime (`docs/ceph-upstream-bugs.md`, "radosgw aborts the process after 100 failed control-watch re-registrations", tracker #80992).
- A zero or negative `rgw_usage_max_shards`, `rgw_usage_max_user_shards` or `rgw_lc_max_objs` is floored to 1 at `Open` with an error-level log line, and no code path divides by a shard count except through `shardMod`, which refuses 0 (`docs/ceph-upstream-bugs.md`, "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards", tracker #80991). GC's count is unit W's.
- Listing-time index suggestions are sent exactly as the cluster's radosgw sends them: unguarded on Squid, behind `assert_exists` and the reshard guard on Tentacle (`docs/ceph-upstream-bugs.md`, "Squid does not guard listing-time index suggestions against resharding", tracker #81000; `docs/exclusions.md`, "Dynamic resharding worker", still required).
- A zonegroup placement target with an empty `storage_classes` is encoded as stored, not with radosgw's inserted `STANDARD` (`docs/ceph-upstream-bugs.md`, "radosgw adds STANDARD to an empty placement target on decode"); the resolver treats an empty set as `{STANDARD}` when it looks a class up.
- Byte-compatibility with radosgw is the acceptance test: object names, attribute names, index and control object names, error codes, messages, headers and XML element order are copied from the C++ cited in each task, not designed.
- Never touch the ambient Kubernetes or Ceph cluster. Cluster tasks use the rooket clusters (`make cluster-up RELEASE=squid|tentacle`, `ROOKET_NAME=rgw-go-<release> rooket kubectl`) and are marked `[cluster]`; radosgw-admin runs in the toolbox through `hack/rooket/lib.sh`'s helpers.
- The librados headers on this machine are absent: cgo builds use `CGO_CFLAGS=-I/home/jhoblitt/github/ceph/src/include` and `CGO_LDFLAGS=-L$HOME/.local/lib/rgw-go`; module downloads need the sandbox disabled.
- The phase-0 gate (`make gate`) stays green: nothing in this plan changes an encoder's bytes.
- Commits are Conventional Commits with the two repository trailers; each task ends in a draft PR assigned to the author, CI watched, merged on green with a merge commit under the standing authorization. Any material change to `docs/exclusions.md` is reported so the rgw-rs session can be told.

## Review Focus

1. **A notify whose payload names an object the gateway never read, or carries a forged `UPDATE_OBJ` body.** The cache must treat it as an invalidation of that key and nothing more: no entry is created from it, no read is served from it. Pinned in Task 3 (`never stores a notify payload`, both ops, a key not present and a key present).
2. **A control watch that dies and stays dead for minutes.** The cache is disabled while any watch is down, every read goes to RADOS, re-registration keeps retrying with growing pauses, and the 101st failure is not special. Pinned in Task 2 (`retries past one hundred failures without stopping, backing off up to thirty seconds and resetting on success`, `disables the cache when a watch is lost and re-enables it when the watch is back`).
3. **A listing whose marker sits inside a common prefix, or whose keys start with `_`.** The marker is fast-forwarded past the prefix with `\xFF`, escaped names round-trip through `parse_raw_oid`, and a namespaced entry never leaks into a plain listing. Pinned in Task 8 (`fast-forwards a marker inside a common prefix`, `lists an object whose name starts with an underscore`, `skips the multipart namespace`).
4. **Two gateways creating the same bucket name at once, or a CreateBucket racing a DeleteBucket.** The loser sees `BucketAlreadyExists` when the owner differs and 200 when it is the same owner, an orphaned instance and index of the loser are cleaned up, and a bucket whose entry point vanished between instance write and link is unlinked rather than left dangling. Pinned in Task 7 (`cleans up the losing instance`, `unlinks when the entry point was deleted concurrently`).
5. **A quota check on a bucket whose stats the cache has never seen, whose owner is an account, or whose `check_on_raw` flag is set.** The first check fetches the shard headers, an account owner's quota comes from the account object, and raw accounting compares `size` while the default compares `size_rounded` plus the 4 KiB-rounded new size. Pinned in Task 9 (`fetches stats on a cold cache once and serves the cache until the ttl`, `uses the account's quotas for an account owner`, `compares rounded size by default and raw size when check_on_raw is set`).

## Decisions this plan fixes

Each is repeated where it applies; this list exists so a reviewer sees them at once, with the source each rests on.

- **M-D1** Zone resolution reads what radosgw's startup reads and creates nothing: no default zonegroup or zone, no master-zone fix-up written back (owner decision 10 kept it read-only). radosgw resolves its zone in `rgw::SiteConfig::load`, called from `AppMain::init_storage` ([`rgw_appmain.cc:221`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_appmain.cc#L221) at v19.2.6, [`:223`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_appmain.cc#L223) at v20.2.4), which on both releases creates a zone and a zonegroup named `default` when `rgw_zone` or `rgw_zonegroup` is unset and no realm exists (`read_or_create_default_zone`, [`driver/rados/rgw_zone.cc:1214`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.cc#L1214), [`:1222`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.cc#L1222) at v19.2.6, [`:1208`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.cc#L1208), [`:1216`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_zone.cc#L1216) at v20.2.4; `read_or_create_default_zonegroup`, [`:1312`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.cc#L1312), [`:1306`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_zone.cc#L1306)). On Squid `RGWSI_Zone::do_start` ([svc_zone.cc:128-297](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L128-L297) at v19.2.6, whose reads `resolveZone` transcribes) creates them again for an unnamed or explicitly `default` zonegroup and zone (`create_default_zg`, [`:214`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L214); `init_default_zone`, [`:240`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L240)). On both releases a zonegroup with one zone and no master is written back with that zone as master (`init_zg_from_period`, [svc_zone.cc:513-533](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L513-L533) at v19.2.6, [`:349-369`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L349-L369) at v20.2.4; `init_zg_from_local`, [`:595-601`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L595-L601), [`:399-405`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_zone.cc#L399-L405)). A missing zone, zonegroup or their name objects is a startup error naming the object. Rook never reaches radosgw's bootstrap: rook v1.20.7 passes `rgw realm`, `rgw zonegroup` and `rgw zone` to every gateway ([`pkg/operator/ceph/object/spec.go:440-442`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/spec.go#L440-L442)) and creates the zonegroup and zone with `--master` before it starts one ([`objectstore.go:470`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/objectstore.go#L470), [`:475`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/objectstore.go#L475); in the zoned shape [`zonegroup/controller.go:257-260`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/zonegroup/controller.go#L257-L260) and [`zone/controller.go:360-363`](https://github.com/rook/rook/blob/v1.20.7/pkg/operator/ceph/object/zone/controller.go#L360-L363)). Beside it stands decision D4: radosgw creates a missing zone pool when it opens it (`rgw_init_ioctx` with its create flag, [`driver/rados/rgw_tools.cc:23-98`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L23-L98) at v19.2.6), at startup and on first use, and rgw-go creates none. Both are differences from radosgw, which Task 13 records in `docs/exclusions.md`'s coexistence section with this reason.
- **M-D2** A control notify of either op is an invalidation (`docs/ceph-upstream-bugs.md`, "radosgw caches any control-pool UPDATE_OBJ notify payload unchecked"). Own writes populate the local cache and distribute `UPDATE_OBJ` with the written record, as radosgw does, so a coexisting radosgw benefits from the payload it trusts.
- **M-D3** The cache is enabled only while every control watch is registered, as radosgw's `_set_enabled` does (`add_watcher`/`remove_watcher`, [svc_notify.cc:343-363](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_notify.cc#L343-L363) at v19.2.6); a lost watch disables and flushes it, and re-registration backs off 1 s doubling to 30 s for ever, resetting on success.
- **M-D4** `rgw_num_control_oids` is honoured as radosgw honours it (`init_watch`, [svc_notify.cc:199-221](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_notify.cc#L199-L221) at v19.2.6): 0 selects the single legacy object `notify` with one watcher, a negative value one watcher on `notify.0`, N > 0 the objects `notify.0` to `notify.N-1`.
- **M-D5** Shard counts are floored to 1 at `Open` with an error-level log line (`docs/ceph-upstream-bugs.md`, "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards", tracker #80991) rather than refusing to start, because under Rook a crash-looping gateway is the worse failure; every modulo runs through `shardMod`.
- **M-D6** `op.ListObjectsParams` gains `AllowUnordered bool` (additive); the listing's disk check consumes `op.ObjectStore.StatObject` through a one-method `headStater` interface so the specs fake it; until R lands, `op.ErrNotImplemented` from it keeps the entry and sends no suggestion.
- **M-D7** `StatsStore.BucketStats` returns the `Main` category only, which is what HEAD bucket reports (`load_bucket_stats`, [rgw_op.cc:3003-3016](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3003-L3016) at v19.2.6); the quota caches keep radosgw's all-category fetch internally ([rgw_quota.cc:278-284](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L278-L284)).
- **M-D8** `get_acls` and `put_acls` are one route name for bucket and object scope while G merges one handler map per unit; M registers both with a scope switch and declares `objectGetACLs`/`objectPutACLs` handler variables that answer 501 until R and W assign them.
- **M-D9** `meta.ObjVersion` does not exist on main although G's Task 1 says `meta` already keeps it; Task 5 adds it when G has not, with struct conversion to and from `version.ObjVersion`.
- **M-D10** The owner-stats full sync worker lists users by iterating the `users.uid` pool through the seam (skipping `.buckets` objects), so M does not depend on N's `MetadataStore.List`.

---

## File structure

```
internal/driver/doc.go                     modify: the package's shape and the coexistence obligations it carries
internal/driver/store.go                   modify: Open resolves the zone, floors the shard counts, builds the pools, cache, notify, workers
internal/driver/options.go                 the rgw_* options M reads, read once at Open (Task 12)
internal/driver/pools.go                   pool handle cache: meta.Pool -> radosclient.Pool
internal/driver/zone.go                    root-pool resolution of realm, period, zonegroup, zone params; Placement (Task 1)
internal/driver/objv.go                    the version tracker: prepareRead, prepareWrite, applyWrite over cls/version (Task 3)
internal/driver/sysobj.go                  versioned read/write/set-attrs/remove of one RADOS object, cache-through (Task 3)
internal/driver/cache.go                   ObjectCache: LRU, flags, negative entries, expiry, enable/disable (Task 3)
internal/driver/notify.go                  control objects, watches with backoff, distribute (Task 2)
internal/driver/user.go                    UserStore except ListUserBuckets; readAccount (Task 4)
internal/driver/bucket.go                  entry point and instance reads and writes; GetBucket, GetBucketInstance, PutBucketInfo, PutBucketAttrs (Task 5)
internal/driver/index.go                   index pool, shard oids, shard headers, Main-category stats (Task 6)
internal/driver/ownerbuckets.go            the cls_user bucket list of an owner, link/unlink, owner stats (Task 6)
internal/driver/bucketops.go               CreateBucket, DeleteBucket, initIndex, cleanIndex, checkBucketEmpty (Task 7)
internal/driver/list.go                    ListObjects: ordered and unordered merge, disk check, suggestions (Task 8)
internal/driver/stats.go                   StatsStore: quota caches, CheckQuota, AdjustStats, owner sync workers (Task 9)
internal/driver/usage.go                   UsageLogger: accumulator, flush worker, usage_log_hash (Task 10)
internal/driver/options.go, shards.go      the options read once; floorShards, shardMod (Task 12)
internal/driver/*_test.go                  specs on fakerados
internal/driver/driverfakes/               counterfeiter fake of HeadStater (Task 8)
internal/testutil/fakerados/               an in-memory seam cluster: steps, watch/notify, class emulators for version, user and rgw (Tasks 1, 3, 6, 8, 10)
internal/meta/objversion.go                meta.ObjVersion when G has not added it (Task 3, M-D9)
internal/meta/bucket_layout.go             modify: export StrHashLinux (Task 2)
internal/meta/objkey.go                    modify: ParseIndexKeyName, the inverse of IndexKeyName (Task 8)
internal/op/bucket.go                      modify: ListObjectsParams.AllowUnordered (Task 8)
internal/op/stats.go                       modify: StatsStore.AdjustStats when W has not added it (Task 9)
internal/op/bucketops.go                   CreateBucket, DeleteBucket, StatBucket, GetBucketLocation, ListObjects ops (Tasks 7, 8)
internal/op/bucketsubres.go                GetBucketACL, PutBucketACL, Get/Put/DeleteBucketPolicy, Get/Put/DeleteBucketTagging ops (Task 11)
internal/op/opfakes/                       regenerated
internal/memstore/bucket.go, stats.go      modify: AllowUnordered, AdjustStats, Main-only BucketStats (Tasks 8, 9)
internal/s3/bucket.go                      modify: bucketHandlers() filled (Tasks 7, 8, 11)
internal/s3/bucketname.go                  valid_s3_bucket_name (Task 7)
internal/s3/listobjects.go                 ListObjects v1 and v2 rendering (Task 8)
internal/s3/acl.go                         get_acls/put_acls with the scope switch and the object handler variables (Task 11)
internal/s3/*_test.go                      handler specs on memstore
test/integration/metadata_test.go          [cluster] zone resolution, notify both ways, oracle checks (Task 13)
hack/rooket/lib.sh                         modify: radosgw_admin helper if absent (Task 13)
docs/exclusions.md                         modify: the M-D1 and D4 differences (Task 13); the cache-notify paragraph gains rgw-go's handling (Task 2)
docs/ceph-upstream-bugs.md                 modify: the four entries' rgw-go lines name the tasks that realize them (Task 13)
```

## Task index

| # | Task | Depends on | Cluster |
|---|---|---|---|
| 1 | Zone, placement and release resolution from the root pool | G Tasks 1, 2, 8 | no |
| 2 | Control objects and the notify watch with bounded re-registration | 1 | no |
| 3 | The sysobj layer and the metadata cache with invalidate-on-notify | 1, 2 | no |
| 4 | User index lookups: uid, access key, email; PutUser, RemoveUser | 3 | no |
| 5 | Bucket entry point and instance under cls_version | 3 | no |
| 6 | The cls_user bucket list, ListUserBuckets and owner stats sync | 3, 5 | no |
| 7 | Bucket Create, Delete, Head and GetLocation: driver, ops, handlers | 4, 5, 6, 8 (`listUnordered` for the emptiness check) | no |
| 8 | ListObjects v1 and v2 with pending-entry reconciliation | 5, 6 (`indexPool`, `shardOIDs`) | no |
| 9 | Stats and quota caches, CheckQuota, AdjustStats, the sync workers | 4, 5, 6 | no |
| 10 | The usage-log accumulator and flush worker | 1 | no |
| 11 | Bucket ACL, policy and tagging subresources | 5, 7, Z Tasks 7, 8, 11 | no |
| 12 | Options, shard-count floors and startup validation | 1 | no |
| 13 | driver.Store assembly, memstore parity, the cluster specs and the oracle | 1 to 12 | `[cluster]` |

Task 1 replaces G's name-only zone and is the root of everything else. Tasks 2 and 3 are the coherence machinery every later task writes through. Tasks 4, 5 and 6 are independent of one another after Task 3; Tasks 8, 9 and 10 are independent of one another after their dependencies; Task 11 needs Z's `acl`, `tags` and `authz` merged. Task 12 is small and can land any time after Task 1; Task 13 closes the unit on the rooket clusters.

---

### Task 1: Zone, placement and release resolution from the root pool

**Files:**
- Create: `internal/driver/zone.go`, `internal/driver/zone_test.go`, `internal/driver/pools.go`, `internal/testutil/fakerados/doc.go`, `internal/testutil/fakerados/cluster.go`, `internal/testutil/fakerados/pool.go`, `internal/testutil/fakerados/steps.go`, `internal/testutil/fakerados/fakerados_suite_test.go`, `internal/testutil/fakerados/pool_test.go`
- Modify: `internal/driver/store.go` (G's `Open`, its `Store` fields and the zone accessors), `internal/driver/store_test.go` (G's name-only specs become resolution specs), `internal/driver/doc.go`

**Interfaces:**
- Consumes: `radosclient.Cluster`, `radosclient.Pool`, `radosclient.NewReadOp`, `denc.NewDecoder`, `denc.ParseRelease`, `meta.RootPool`, the `meta` root-object OID helpers (`RealmNameOID`, `RealmOID`, `DefaultRealmOID`, `PeriodLatestEpochOID`, `PeriodOID`, `ZoneNameOID`, `ZoneInfoOID`, `DefaultZoneOID`, `ZoneGroupNameOID`, `ZoneGroupInfoOID`, `DefaultZoneGroupOID`, `PeriodConfigOID`), `meta.DecodeNameToID`, `meta.DecodeDefaultSystemMetaObjInfo`, `meta.DecodeRealm`, `meta.DecodePeriodLatestEpochInfo`, `meta.DecodePeriod`, `meta.DecodeZoneParams`, `meta.DecodeZoneGroup`, `meta.DecodePeriodConfig`, `cephconf.Options.String`, G's `driver.Options`, `op.Placement`, `op.ErrInvalidLocationConstraint`.
- Produces:

```go
package driver

// zoneNames are the rgw_* options that name the zone, read once at Open.
type zoneNames struct {
	Realm, RealmID         string // rgw_realm, rgw_realm_id
	ZoneGroup, ZoneGroupID string // rgw_zonegroup, rgw_zonegroup_id
	Zone, ZoneID           string // rgw_zone, rgw_zone_id
}

// rootPools are the pools the four kinds of root object live in; all four
// default to .rgw.root (rgw_realm_root_pool, rgw_zonegroup_root_pool,
// rgw_zone_root_pool, rgw_period_root_pool).
type rootPools struct{ realm, zonegroup, zone, period radosclient.Pool }

// zoneConfig is what RGWSI_Zone::do_start leaves behind when it only reads.
type zoneConfig struct {
	Realm        meta.Realm
	HasRealm     bool
	Period       meta.Period
	HasPeriod    bool
	FromPeriod   bool // the zonegroup came from the period map, not zonegroup_info
	ZoneGroup    meta.ZoneGroup
	Zone         meta.Zone
	Params       meta.ZoneParams
	PeriodConfig meta.PeriodConfig
}

// ErrNoZone wraps every "the zone this gateway serves is not in the root
// pool" failure; the message names the object looked for.
var ErrNoZone = errors.New("driver: zone not found in the root pool")

func resolveZone(ctx context.Context, pools rootPools, names zoneNames) (*zoneConfig, error)

// poolCache hands out one radosclient.Pool per (name, namespace) for the
// life of the Store.
type poolCache struct{ /* mu, cluster, pools map[meta.Pool]radosclient.Pool */ }
func newPoolCache(c radosclient.Cluster) *poolCache
func (c *poolCache) get(ctx context.Context, p meta.Pool) (radosclient.Pool, error)
func (c *poolCache) closeAll() error

// Store gains these fields.
type Store struct {
	cluster radosclient.Cluster
	conf    *cephconf.Options
	release denc.Release
	zone    *zoneConfig
	pools   *poolCache
	// ... the existing worker fields
}
// The op.ZoneInfo methods now return the resolved objects, and Placement is real.
func (s *Store) Placement(rule meta.PlacementRule) (op.Placement, error)
```

```go
package fakerados

// Cluster is an in-memory radosclient.Cluster whose pools execute the seam's
// steps against stored objects, dispatch class calls to registered
// emulators, and deliver notifies to registered watches. It exists so the
// driver's specs assert the exact operations rgw-go composes without a
// cluster; it is not a RADOS.
type Cluster struct{ /* mu, release string, pools map[key]*Pool, classes map[string]ClassFunc, config map[string]string */ }
func New() *Cluster
func (c *Cluster) SetRequiredOSDRelease(name string)
func (c *Cluster) SetConfig(name, value string)               // ConfigGet answers from it; unknown → cephconf.ErrUnknownOption
func (c *Cluster) RegisterClass(class string, fn ClassFunc)   // one emulator per class name
func (c *Cluster) Object(pool, ns, oid string) *Object        // creates the pool entry on first use; nil when the object is absent
func (c *Cluster) Put(pool, ns, oid string, data []byte)      // seeds an object, version 1
func (c *Cluster) Watches(pool, ns, oid string) int
func (c *Cluster) BreakWatches(pool, ns, oid string, err error) // every watch on the object reports err on Err() once
func (c *Cluster) FailWatch(pool, ns, oid string, n int, err error) // the next n Watch calls fail with err
func (c *Cluster) FailNotify(pool, ns, oid string, n int, err error) // the next n Notify calls fail with err (their acks empty)
func (c *Cluster) Notifies(pool, ns, oid string) [][]byte      // payloads delivered so far, oldest first

// Object is one stored object.
type Object struct {
	Data    []byte
	Xattrs  map[string][]byte
	Omap    map[string][]byte
	OmapHdr []byte
	Version uint64
}

// ClassFunc emulates one class: it sees the object (nil when absent; it may
// create it by returning created=true), the method and the input, and
// returns the output and the method's return value (negative errno fails
// the op with that errno).
type ClassFunc func(obj *Object, method string, in []byte) (out []byte, rval int32, created bool)
```

`fakerados` is a non-test package under `internal/testutil` so `driver`, and later R and W, import it from `_test.go` files; its own suite pins the step semantics.

- [ ] **Step 1: Write the failing resolution specs**

`internal/driver/driver_suite_test.go` exists from G. `internal/driver/zone_test.go`, package `driver_test`, seeds a fake cluster the way Rook's `ceph-objectstore` store looks (realm, zonegroup and zone share the store's name; the period is committed; the zone's pools are namespaces of shared pools) and drives `driver.Open`:

```go
package driver_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/jhoblitt/rgw-go/internal/cephconf"
	"github.com/jhoblitt/rgw-go/internal/denc"
	"github.com/jhoblitt/rgw-go/internal/driver"
	"github.com/jhoblitt/rgw-go/internal/meta"
	"github.com/jhoblitt/rgw-go/internal/op"
	"github.com/jhoblitt/rgw-go/internal/testutil/fakerados"
)

// encode renders a meta type as radosgw stores it at the Squid release.
func encode(v interface{ Encode(*denc.Encoder, denc.Release) }) []byte {
	e := denc.NewEncoder()
	v.Encode(e, denc.Squid)
	return e.Bytes()
}

// rookZone seeds the root pool with a single-zone realm in Rook's zoned shape
// and returns the ids it chose.
type rookZone struct{ realmID, zgID, zoneID, periodID string }

func seedRookZone(c *fakerados.Cluster, store string, withPeriod bool) rookZone {
	ids := rookZone{realmID: "realm-" + store, zgID: "zg-" + store, zoneID: "zone-" + store, periodID: "period-" + store}
	zone := meta.NewZone()
	zone.ID, zone.Name = ids.zoneID, store
	zg := meta.ZoneGroup{ID: ids.zgID, Name: store, APIName: store, IsMaster: true, MasterZone: ids.zoneID,
		Zones: map[string]meta.Zone{ids.zoneID: zone}, RealmID: ids.realmID,
		DefaultPlacement: meta.PlacementRule{Name: "default-placement"},
		PlacementTargets: map[string]meta.ZoneGroupPlacementTarget{"default-placement": {Name: "default-placement", StorageClasses: []string{"STANDARD"}}}}
	params := meta.ZoneParams{ID: ids.zoneID, Name: store, RealmID: ids.realmID,
		DomainRoot:  meta.ParsePool(store + ".rgw.meta:root"),
		ControlPool: meta.ParsePool(store + ".rgw.control"),
		UserUIDPool: meta.ParsePool(store + ".rgw.meta:users.uid"),
		UserKeysPool: meta.ParsePool(store + ".rgw.meta:users.keys"),
		UserEmailPool: meta.ParsePool(store + ".rgw.meta:users.email"),
		LogPool: meta.ParsePool(store + ".rgw.log"), UsageLogPool: meta.ParsePool(store + ".rgw.log:usage"),
		PlacementPools: map[string]meta.ZonePlacementInfo{"default-placement": {
			IndexPool: meta.ParsePool(store + ".rgw.buckets.index"),
			DataExtraPool: meta.ParsePool(store + ".rgw.buckets.non-ec"),
			StorageClasses: meta.ZoneStorageClasses{"STANDARD": {DataPool: ptr(meta.ParsePool(store + ".rgw.buckets.data"))}},
		}}}
	realm := meta.Realm{ID: ids.realmID, Name: store, Epoch: 1}
	if withPeriod {
		realm.CurrentPeriod = ids.periodID
	}
	root := meta.RootPool
	c.Put(root, "", meta.RealmNameOID(store), encode(meta.NameToID{ObjID: ids.realmID}))
	c.Put(root, "", meta.RealmOID(ids.realmID), encode(realm))
	c.Put(root, "", meta.DefaultRealmOID(), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.realmID}))
	c.Put(root, "", meta.ZoneNameOID(store), encode(meta.NameToID{ObjID: ids.zoneID}))
	c.Put(root, "", meta.ZoneInfoOID(ids.zoneID), encode(params))
	c.Put(root, "", meta.DefaultZoneOID(ids.realmID), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.zoneID}))
	c.Put(root, "", meta.ZoneGroupNameOID(store), encode(meta.NameToID{ObjID: ids.zgID}))
	c.Put(root, "", meta.ZoneGroupInfoOID(ids.zgID), encode(zg))
	c.Put(root, "", meta.DefaultZoneGroupOID(ids.realmID), encode(meta.DefaultSystemMetaObjInfo{DefaultID: ids.zgID}))
	if withPeriod {
		period := meta.NewPeriod()
		period.ID, period.Epoch, period.RealmID = ids.periodID, 1, ids.realmID
		period.MasterZoneGroup, period.MasterZone = ids.zgID, ids.zoneID
		period.PeriodMap = meta.PeriodMap{ID: ids.periodID, ZoneGroups: map[string]meta.ZoneGroup{ids.zgID: zg}, MasterZoneGroup: ids.zgID}
		period.PeriodConfig.BucketQuota = meta.Quota{MaxSize: -1, MaxObjects: 7, Enabled: true}
		c.Put(root, "", meta.PeriodLatestEpochOID(ids.periodID), encode(meta.PeriodLatestEpochInfo{Epoch: 1}))
		c.Put(root, "", meta.PeriodOID(ids.periodID, 1), encode(period))
	}
	return ids
}

func ptr[T any](v T) *T { return &v }

func conf(kv map[string]string) *cephconf.Options {
	m := cephconf.MapGetter{"rgw_realm": "", "rgw_realm_id": "", "rgw_zonegroup": "", "rgw_zonegroup_id": "", "rgw_zone": "", "rgw_zone_id": "",
		"rgw_realm_root_pool": ".rgw.root", "rgw_zonegroup_root_pool": ".rgw.root", "rgw_zone_root_pool": ".rgw.root", "rgw_period_root_pool": ".rgw.root",
		"rgw_num_control_oids": "8", "rgw_max_notify_retries": "10", "rgw_cache_enabled": "true", "rgw_cache_lru_size": "25000", "rgw_cache_expiry_interval": "900",
		"rgw_usage_max_shards": "32", "rgw_usage_max_user_shards": "1", "rgw_lc_max_objs": "32", "rgw_enable_usage_log": "false",
		"rgw_usage_log_flush_threshold": "1024", "rgw_usage_log_tick_interval": "30", "rgw_bucket_quota_ttl": "600", "rgw_bucket_quota_cache_size": "10000",
		"rgw_user_quota_bucket_sync_interval": "180", "rgw_user_quota_sync_interval": "86400", "rgw_user_quota_sync_idle_users": "false", "rgw_user_quota_sync_wait_time": "86400",
		"rgw_enable_quota_threads": "true", "rgw_bucket_default_quota_max_objects": "-1", "rgw_bucket_default_quota_max_size": "-1",
		"rgw_user_default_quota_max_objects": "-1", "rgw_user_default_quota_max_size": "-1", "rgw_account_default_quota_max_objects": "-1", "rgw_account_default_quota_max_size": "-1",
		"rgw_list_bucket_min_readahead": "1000", "rgw_max_listing_results": "1000", "rgw_override_bucket_index_max_shards": "0", "rgw_bucket_index_max_aio": "128",
		"rgw_dynamic_resharding": "true", "rgw_list_buckets_max_chunk": "1000", "rgw_relaxed_s3_bucket_names": "false", "rgw_max_put_param_size": "1048576",
		"rgw_acl_grants_max_num": "100", "rgw_run_sync_thread": "true", "rgw_max_chunk_size": "4194304"}
	for k, v := range kv {
		m[k] = v
	}
	return cephconf.NewOptions(m)
}

var _ = Describe("zone resolution", func() {
	var c *fakerados.Cluster
	BeforeEach(func() {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
	})
	It("resolves Rook's zoned store from the period when the names are configured", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_realm": "ceph-objectstore", "rgw_zonegroup": "ceph-objectstore", "rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Release()).To(Equal(denc.Squid))
		Expect(s.Realm().ID).To(Equal(ids.realmID))
		Expect(s.Period().ID).To(Equal(ids.periodID))
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
		Expect(s.Zone().ID).To(Equal(ids.zoneID))
		Expect(s.ZoneParams().DomainRoot).To(Equal(meta.ParsePool("ceph-objectstore.rgw.meta:root")))
		Expect(s.PeriodConfig().BucketQuota.MaxObjects).To(BeEquivalentTo(7), "period config comes from the period")
	})
	It("falls back to the default realm, zonegroup and zone objects when no name is configured", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		s, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Zone().ID).To(Equal(ids.zoneID))
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
	})
	It("uses the local zonegroup and reads period_config when the realm has no period", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		pc := meta.PeriodConfig{UserQuota: meta.Quota{MaxSize: 4096, MaxObjects: -1, Enabled: true}}
		c.Put(meta.RootPool, "", meta.PeriodConfigOID(ids.realmID), encode(pc))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Period().ID).To(BeEmpty())
		Expect(s.ZoneGroup().ID).To(Equal(ids.zgID))
		Expect(s.PeriodConfig().UserQuota.MaxSize).To(BeEquivalentTo(4096))
	})
	It("takes the zonegroup from the period map, not zonegroup_info, when both exist and differ", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", true)
		stale := meta.ZoneGroup{ID: ids.zgID, Name: "ceph-objectstore", APIName: "stale", RealmID: ids.realmID}
		c.Put(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID), encode(stale))
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.ZoneGroup().APIName).To(Equal("ceph-objectstore"), "the committed period wins")
	})
	It("refuses to start when the configured zone does not exist, naming the object", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "missing"}), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(err.Error()).To(ContainSubstring("zone_names.missing"))
	})
	It("refuses to start when rgw_zonegroup names a different zonegroup", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore", "rgw_zonegroup": "other"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring(`zonegroup "ceph-objectstore" is not "other"`)))
	})
	It("refuses to start when the zone is not a member of its zonegroup", func(ctx SpecContext) {
		ids := seedRookZone(c, "ceph-objectstore", false)
		zg := meta.ZoneGroup{ID: ids.zgID, Name: "ceph-objectstore", RealmID: ids.realmID, Zones: map[string]meta.Zone{}}
		c.Put(meta.RootPool, "", meta.ZoneGroupInfoOID(ids.zgID), encode(zg))
		_, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(ContainSubstring("is not in zonegroup")))
	})
	It("never creates a zone or zonegroup on an empty root pool", func(ctx SpecContext) {
		_, err := driver.Open(ctx, c, conf(nil), driver.Options{})
		Expect(err).To(MatchError(driver.ErrNoZone))
		Expect(c.Object(meta.RootPool, "", meta.ZoneNameOID("default"))).To(BeNil(), "radosgw would have bootstrapped one; rgw-go must not")
	})
	It("honours the release override and the Squid floor as G left them", func(ctx SpecContext) {
		seedRookZone(c, "ceph-objectstore", true)
		t := denc.Tentacle
		s, err := driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{Release: &t})
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Release()).To(Equal(denc.Tentacle))
		c.SetRequiredOSDRelease("reef")
		_, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).To(MatchError(radosclient.ErrReleaseTooOld))
	})
})

var _ = Describe("Placement", func() {
	var s *driver.Store
	BeforeEach(func(ctx SpecContext) {
		c := fakerados.New()
		c.SetRequiredOSDRelease("squid")
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
	})
	It("resolves the STANDARD class of a rule", func() {
		p, err := s.Placement(meta.PlacementRule{Name: "default-placement"})
		Expect(err).NotTo(HaveOccurred())
		Expect(p.Rule).To(Equal(meta.PlacementRule{Name: "default-placement", StorageClass: "STANDARD"}))
		Expect(p.DataPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.data")))
		Expect(p.IndexPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.index")))
		Expect(p.DataExtraPool).To(Equal(meta.ParsePool("ceph-objectstore.rgw.buckets.non-ec")))
		Expect(p.Compression).To(BeEmpty())
	})
	It("is InvalidLocationConstraint for an unknown rule or class", func() {
		_, err := s.Placement(meta.PlacementRule{Name: "nope"})
		Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
		_, err = s.Placement(meta.PlacementRule{Name: "default-placement", StorageClass: "GLACIER"})
		Expect(err).To(MatchError(op.ErrInvalidLocationConstraint))
	})
})
```

The imports of `zone_test.go` also include `"github.com/jhoblitt/rgw-go/internal/radosclient"` for `ErrReleaseTooOld`. Two more placement specs follow in the same file with a second seed: one sets `StorageClasses["COMP_ZLIB"] = {DataPool: <data pool>, CompressionType: ptr("zlib")}` and asserts `p.Compression == "zlib"` for the rule `default-placement/COMP_ZLIB`; one leaves `DataExtraPool` empty and asserts it falls back to STANDARD's data pool ([`rgw_zone_types.h:274-280`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_zone_types.h#L274-L280)).

- [ ] **Step 2: Run to see the suite fail to compile**

```sh
go test -tags=ceph_preview ./internal/driver/
```

Expected: undefined `fakerados`, `driver.ErrNoZone`, `s.PeriodConfig`.

- [ ] **Step 3: Write `fakerados`**

`internal/testutil/fakerados/steps.go` executes one seam step against an object. The rules are RADOS's: a write op is atomic (the first failing step aborts it and nothing is applied); `StepFlagFailOK` set by a `StepFlagsStep` applies to the step that follows it; `Create(exclusive)` on an existing object is `EEXIST` when exclusive and a no-op otherwise; `Remove` of an absent object is `ENOENT`; any read step or `AssertExists` on an absent object is `ENOENT`; `AssertVersion(v)` mismatching is `ERANGE` when the stored version is larger and `EOVERFLOW` (75) otherwise, as librados reports them; a successful write op increments `Version`; `Read` on an absent object is `ENOENT` and a short read returns what exists; `Exec` dispatches to the class emulator by class name, an unregistered class is `EOPNOTSUPP`, and a negative `rval` fails the op with that errno. Errors are `*radosclient.Error{Errno, Op}` so the seam's sentinels match through `errors.Is`.

```go
package fakerados

// apply runs the steps of one op against obj (nil when absent) and returns
// the object after the op, or the first error. It mutates a copy so a failed
// write leaves the stored object untouched.
func (p *Pool) apply(oid string, steps []radosclient.Step, write bool) (*Object, error) {
	cur := p.objects[oid]
	var obj *Object
	if cur != nil {
		obj = cur.clone()
	}
	failOK := false
	for _, st := range steps {
		if f, ok := st.(*radosclient.StepFlagsStep); ok {
			failOK = f.Flags&radosclient.StepFlagFailOK != 0
			continue
		}
		var err error
		obj, err = p.step(oid, obj, st)
		if err != nil && !failOK {
			return nil, err
		}
		failOK = false
	}
	if write {
		if obj != nil {
			obj.Version++
			p.objects[oid] = obj
		} else {
			delete(p.objects, oid)
		}
	}
	return obj, nil
}

func errno(op string, n syscall.Errno) error { return &radosclient.Error{Errno: int32(n), Op: op} }

func (p *Pool) step(oid string, obj *Object, st radosclient.Step) (*Object, error) {
	switch s := st.(type) {
	case *radosclient.CreateStep:
		if obj != nil {
			if s.Exclusive {
				return obj, errno("create", syscall.EEXIST)
			}
			return obj, nil
		}
		return newObject(), nil
	case *radosclient.RemoveStep:
		if obj == nil {
			return nil, errno("remove", syscall.ENOENT)
		}
		return nil, nil
	case *radosclient.AssertExistsStep:
		if obj == nil {
			return nil, errno("assert_exists", syscall.ENOENT)
		}
		return obj, nil
	case *radosclient.AssertVersionStep:
		if obj == nil {
			return nil, errno("assert_version", syscall.ENOENT)
		}
		switch {
		case obj.Version > s.Version:
			return obj, errno("assert_version", syscall.ERANGE)
		case obj.Version < s.Version:
			return obj, errno("assert_version", syscall.EOVERFLOW)
		}
		return obj, nil
	case *radosclient.WriteFullStep:
		if obj == nil {
			obj = newObject()
		}
		obj.Data = slices.Clone(s.Data)
		return obj, nil
	case *radosclient.SetXattrStep:
		if obj == nil {
			obj = newObject()
		}
		obj.Xattrs[s.Name] = slices.Clone(s.Value)
		return obj, nil
	case *radosclient.RmXattrStep:
		if obj == nil {
			return nil, errno("rmxattr", syscall.ENOENT)
		}
		delete(obj.Xattrs, s.Name)
		return obj, nil
	case *radosclient.ReadStep:
		if obj == nil {
			s.Result.Err = errno("read", syscall.ENOENT)
			return nil, s.Result.Err
		}
		end := min(uint64(len(obj.Data)), s.Offset+s.Length)
		if s.Offset > uint64(len(obj.Data)) {
			end = s.Offset
		}
		s.Result.Data = slices.Clone(obj.Data[min(s.Offset, uint64(len(obj.Data))):end])
		s.Result.N = len(s.Result.Data)
		return obj, nil
	case *radosclient.StatStep:
		if obj == nil {
			s.Result.Err = errno("stat", syscall.ENOENT)
			return nil, s.Result.Err
		}
		s.Result.Size = uint64(len(obj.Data))
		s.Result.ModTime = obj.mtime
		return obj, nil
	case *radosclient.GetXattrsStep:
		if obj == nil {
			s.Result.Err = errno("getxattrs", syscall.ENOENT)
			return nil, s.Result.Err
		}
		s.Result.Xattrs = maps.Clone(obj.Xattrs)
		return obj, nil
	case *radosclient.OmapSetStep, *radosclient.OmapRmKeysStep, *radosclient.OmapClearStep,
		*radosclient.OmapGetValsStep, *radosclient.OmapGetValsByKeysStep, *radosclient.OmapGetKeysStep, *radosclient.OmapCmpStep:
		return p.omapStep(obj, st)
	case *radosclient.ExecStep:
		fn, ok := p.cluster.classes[s.Class]
		if !ok {
			s.Result.Set(nil, -int32(syscall.EOPNOTSUPP))
			return obj, errno("exec "+s.Class+"."+s.Method, syscall.EOPNOTSUPP)
		}
		out, rval, created := fn(obj, s.Method, s.In)
		if obj == nil && created {
			obj = newObject()
		}
		s.Result.Set(out, rval)
		if rval < 0 {
			return obj, &radosclient.Error{Errno: -rval, Op: "exec " + s.Class + "." + s.Method}
		}
		return obj, nil
	default:
		return obj, radosclient.ErrBadOp
	}
}
```

Write `omapStep` for the seven omap steps over `obj.Omap` (sorted keys, `StartAfter` exclusive, `Max` with `More`, `OmapCmp` with the six `CmpOp`s on the stored value, `OmapClear`), `newObject()` (`Xattrs` and `Omap` allocated, `mtime` = the cluster's clock), and the `WriteStep`, `AppendStep`, `ZeroStep`, `TruncateStep`, `CmpXattrStep`, `SetAllocHintStep` (no-op) cases in the same switch; each follows librados (`CmpXattr` mismatch is `ECANCELED`).

`internal/testutil/fakerados/pool.go`: `Pool` implements `radosclient.Pool` with `Name`, `Namespace`, `ID` (a stable small integer per pool), `WithLocator` (returns the same pool: locators do not change the fake's placement), `Read`/`Write` (lock the cluster, `apply`, return `obj.Version`), `ListObjects` (sorted oids), `Watch`/`Notify`/`Unwatch` and the lock methods (`LockExclusive` etc. return `radosclient.ErrNotSupported`; P adds them). `Watch` registers `fn` under the oid and returns a `*watch` whose `Err()` channel `BreakWatches` feeds and whose `Close` deregisters it; a `FailWatch` budget makes the next n `Watch` calls return the injected error. `Notify` appends the payload to `notifies[oid]`, then calls every registered `fn(id, notifierID, payload)` synchronously (each watch a distinct `notifierID`, `id` increasing) and returns one `radosclient.NotifyAck` per watch, or the injected `FailNotify` error with no acks.

`internal/testutil/fakerados/cluster.go`: `Cluster` implements `radosclient.Cluster`: `Pool(ctx, name, ns)` returns the one `*Pool` per `(name, ns)`, creating it (the fake has no pool-existence failures; the driver never creates pools, so a spec that wants a missing pool uses `c.FailPool(name)`, which makes `Pool` return `radosclient.ErrNotFound`); `MonCommand` returns `radosclient.ErrNotSupported`; `ConfigGet` answers from `SetConfig` or `cephconf.ErrUnknownOption`; `RequiredOSDRelease` returns the set name; `Close` marks closed.

`internal/testutil/fakerados/pool_test.go` pins: exclusive create on an existing object is `radosclient.ErrExists`; a failing second step leaves the object unchanged; `FailOK` skips one failure; `Version` increments per write op; a class emulator's negative rval fails the op with that errno; `Notify` reaches two watches and returns two acks; `BreakWatches` delivers on `Err()` once; `FailWatch(n)` fails exactly n registrations.

- [ ] **Step 4: Write `zone.go` and `pools.go`; rewrite `Open`**

`internal/driver/pools.go`:

```go
package driver

// poolCache hands out one radosclient.Pool per (name, namespace) for the
// life of the Store; Close releases them all.
type poolCache struct {
	mu      sync.Mutex
	cluster radosclient.Cluster
	pools   map[meta.Pool]radosclient.Pool
}

func newPoolCache(c radosclient.Cluster) *poolCache {
	return &poolCache{cluster: c, pools: map[meta.Pool]radosclient.Pool{}}
}

func (c *poolCache) get(ctx context.Context, p meta.Pool) (radosclient.Pool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if pool, ok := c.pools[p]; ok {
		return pool, nil
	}
	pool, err := c.cluster.Pool(ctx, p.Name, p.NS)
	if err != nil {
		return nil, fmt.Errorf("opening pool %s: %w", p, err)
	}
	c.pools[p] = pool
	return pool, nil
}

func (c *poolCache) closeAll() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for k, p := range c.pools {
		errs = append(errs, p.Close())
		delete(c.pools, k)
	}
	return errors.Join(errs...)
}
```

`internal/driver/zone.go`:

```go
package driver

// rootObjectMax bounds a root-pool read; rgw_max_chunk_size is the same 4 MiB,
// and every realm, period, zonegroup and zone object is far smaller.
const rootObjectMax = 4 << 20

// readWhole returns the bytes of one small object.
func readWhole(ctx context.Context, p radosclient.Pool, oid string) ([]byte, error) {
	rop := radosclient.NewReadOp()
	res := rop.Read(0, rootObjectMax)
	if _, err := p.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		return nil, fmt.Errorf("reading %s/%s: %w", p.Name(), oid, err)
	}
	if res.N >= rootObjectMax {
		return nil, fmt.Errorf("reading %s/%s: object exceeds %d bytes", p.Name(), oid, rootObjectMax)
	}
	return res.Data[:res.N], nil
}

// decodeRoot decodes one root object, naming it on failure.
func decodeRoot[T any](b []byte, dec func(*denc.Decoder) T, oid string) (T, error) {
	d := denc.NewDecoder(b)
	v := dec(d)
	if err := d.Err(); err != nil {
		var zero T
		return zero, fmt.Errorf("decoding %s: %w", oid, err)
	}
	return v, nil
}

// readID reads a name-to-id object (zone_names.<name> and the like).
func readID(ctx context.Context, p radosclient.Pool, oid string) (string, error) {
	b, err := readWhole(ctx, p, oid)
	if err != nil {
		return "", err
	}
	v, err := decodeRoot(b, meta.DecodeNameToID, oid)
	return v.ObjID, err
}

// readDefaultID reads a default.<kind>[.<realm>] object.
func readDefaultID(ctx context.Context, p radosclient.Pool, oid string) (string, error) {
	b, err := readWhole(ctx, p, oid)
	if err != nil {
		return "", err
	}
	v, err := decodeRoot(b, meta.DecodeDefaultSystemMetaObjInfo, oid)
	return v.DefaultID, err
}

// resolveZone is RGWSI_Zone::do_start (svc_zone.cc:128-297 at v19.2.6)
// reading only: the realm (ENOENT tolerated), its current period (ENOENT
// tolerated), the zone params, the zonegroup from the period map when the
// zone is in it and from zonegroup_info otherwise, the zone's public config,
// and the period config. It never creates a default zonegroup or zone
// (radosgw bootstraps them on an empty root pool; see docs/exclusions.md)
// and never searches other realms, because a Rook cluster has one.
func resolveZone(ctx context.Context, pools rootPools, names zoneNames) (*zoneConfig, error) {
	zc := &zoneConfig{}
	realmID := names.RealmID
	if realmID == "" {
		var err error
		if names.Realm != "" {
			realmID, err = readID(ctx, pools.realm, meta.RealmNameOID(names.Realm))
		} else {
			realmID, err = readDefaultID(ctx, pools.realm, meta.DefaultRealmOID())
		}
		if err != nil && !errors.Is(err, radosclient.ErrNotFound) {
			return nil, err
		}
	}
	if realmID != "" {
		oid := meta.RealmOID(realmID)
		b, err := readWhole(ctx, pools.realm, oid)
		switch {
		case err == nil:
			if zc.Realm, err = decodeRoot(b, meta.DecodeRealm, oid); err != nil {
				return nil, err
			}
			zc.HasRealm = true
		case errors.Is(err, radosclient.ErrNotFound):
		default:
			return nil, err
		}
	}
	if zc.HasRealm && zc.Realm.CurrentPeriod != "" {
		if err := readPeriod(ctx, pools.period, zc); err != nil {
			return nil, err
		}
	}
	zoneID := names.ZoneID
	if zoneID == "" {
		var oid string
		switch {
		case names.Zone != "":
			oid = meta.ZoneNameOID(names.Zone)
		case zc.HasRealm:
			oid = meta.DefaultZoneOID(zc.Realm.ID)
		default:
			oid = meta.ZoneNameOID("default")
		}
		var err error
		if names.Zone != "" || !zc.HasRealm {
			zoneID, err = readID(ctx, pools.zone, oid)
		} else {
			zoneID, err = readDefaultID(ctx, pools.zone, oid)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %w", ErrNoZone, oid, err)
		}
	}
	oid := meta.ZoneInfoOID(zoneID)
	b, err := readWhole(ctx, pools.zone, oid)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrNoZone, oid, err)
	}
	if zc.Params, err = decodeRoot(b, meta.DecodeZoneParams, oid); err != nil {
		return nil, err
	}
	if zc.HasPeriod {
		for _, zg := range zc.Period.PeriodMap.ZoneGroups {
			if _, ok := zg.Zones[zc.Params.ID]; ok {
				zc.ZoneGroup, zc.FromPeriod = zg, true
				zc.PeriodConfig = zc.Period.PeriodConfig
				break
			}
		}
	}
	if !zc.FromPeriod {
		if err := readLocalZoneGroup(ctx, pools, names, zc); err != nil {
			return nil, err
		}
	}
	if names.ZoneGroup != "" && zc.ZoneGroup.Name != names.ZoneGroup {
		return nil, fmt.Errorf("driver: zonegroup %q is not %q (rgw_zonegroup)", zc.ZoneGroup.Name, names.ZoneGroup)
	}
	zone, ok := zc.ZoneGroup.Zones[zc.Params.ID]
	if !ok {
		return nil, fmt.Errorf("driver: zone %s (%s) is not in zonegroup %s (%s)", zc.Params.ID, zc.Params.Name, zc.ZoneGroup.ID, zc.ZoneGroup.Name)
	}
	zc.Zone = zone
	return zc, nil
}

// readPeriod is RGWPeriod::init with an empty epoch: the latest epoch object,
// then the period at that epoch (rgw_period.cc:14-46, :307-345).
func readPeriod(ctx context.Context, p radosclient.Pool, zc *zoneConfig) error {
	id := zc.Realm.CurrentPeriod
	oid := meta.PeriodLatestEpochOID(id)
	b, err := readWhole(ctx, p, oid)
	if errors.Is(err, radosclient.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	latest, err := decodeRoot(b, meta.DecodePeriodLatestEpochInfo, oid)
	if err != nil {
		return err
	}
	oid = meta.PeriodOID(id, latest.Epoch)
	if b, err = readWhole(ctx, p, oid); err != nil {
		if errors.Is(err, radosclient.ErrNotFound) {
			return nil
		}
		return err
	}
	if zc.Period, err = decodeRoot(b, meta.DecodePeriod, oid); err != nil {
		return err
	}
	zc.HasPeriod = true
	return nil
}

// readLocalZoneGroup is RGWZoneGroup::init without the period, then
// RGWPeriodConfig::read (svc_zone.cc:196-204, :287-295).
func readLocalZoneGroup(ctx context.Context, pools rootPools, names zoneNames, zc *zoneConfig) error {
	zgID := names.ZoneGroupID
	if zgID == "" {
		var oid string
		var err error
		switch {
		case names.ZoneGroup != "":
			oid = meta.ZoneGroupNameOID(names.ZoneGroup)
			zgID, err = readID(ctx, pools.zonegroup, oid)
		case zc.HasRealm:
			oid = meta.DefaultZoneGroupOID(zc.Realm.ID)
			zgID, err = readDefaultID(ctx, pools.zonegroup, oid)
		default:
			oid = meta.ZoneGroupNameOID("default")
			zgID, err = readID(ctx, pools.zonegroup, oid)
		}
		if err != nil {
			return fmt.Errorf("%w: %s: %w", ErrNoZone, oid, err)
		}
	}
	oid := meta.ZoneGroupInfoOID(zgID)
	b, err := readWhole(ctx, pools.zonegroup, oid)
	if err != nil {
		return fmt.Errorf("%w: %s: %w", ErrNoZone, oid, err)
	}
	if zc.ZoneGroup, err = decodeRoot(b, meta.DecodeZoneGroup, oid); err != nil {
		return err
	}
	realmID := ""
	if zc.HasRealm {
		realmID = zc.Realm.ID
	}
	oid = meta.PeriodConfigOID(realmID)
	b, err = readWhole(ctx, pools.period, oid)
	switch {
	case err == nil:
		zc.PeriodConfig, err = decodeRoot(b, meta.DecodePeriodConfig, oid)
		return err
	case errors.Is(err, radosclient.ErrNotFound):
		return nil
	default:
		return err
	}
}

// Placement is rgw::find_zone_placement plus RGWZonePlacementInfo's pool
// accessors (driver/rados/rgw_zone.cc:1090-1110, rgw_zone_types.h:274-303 at
// v19.2.6): the rule must name a placement of this zone and a storage class
// it has; the data pool is the class's, the extra pool the explicit one or
// STANDARD's data pool, the compression the class's.
func (s *Store) Placement(rule meta.PlacementRule) (op.Placement, error) {
	pi, ok := s.zone.Params.PlacementPools[rule.Name]
	if !ok {
		return op.Placement{}, fmt.Errorf("placement %q: %w", rule.Name, op.ErrInvalidLocationConstraint)
	}
	sc := rule.CanonicalStorageClass()
	class, ok := pi.StorageClasses[sc]
	if !ok {
		return op.Placement{}, fmt.Errorf("placement %q storage class %q: %w", rule.Name, sc, op.ErrInvalidLocationConstraint)
	}
	std := pi.StorageClasses[meta.StorageClassStandard]
	stdPool := deref(std.DataPool, meta.Pool{})
	extra := pi.DataExtraPool
	if extra.Name == "" {
		extra = stdPool
	}
	return op.Placement{
		Rule:          meta.PlacementRule{Name: rule.Name, StorageClass: sc},
		DataPool:      deref(class.DataPool, stdPool),
		IndexPool:     pi.IndexPool,
		DataExtraPool: extra,
		Compression:   deref(class.CompressionType, ""),
		InlineData:    pi.InlineData,
	}, nil
}

func deref[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}
```

`internal/driver/store.go`: `Open` keeps G's release detection, then reads the six names and four root pool options through `conf.String`, opens the root pools through `newPoolCache`, calls `resolveZone`, and stores the result; the `op.ZoneInfo` accessors return `s.zone.Realm`, `s.zone.Period`, `s.zone.ZoneGroup`, `s.zone.Zone`, `s.zone.Params`; add `func (s *Store) PeriodConfig() meta.PeriodConfig` (Task 9 reads the default quotas from it) and `func (s *Store) Close() error` (the pool cache). Every other store method still returns `op.ErrNotImplemented` until its task. Log one `slog.InfoContext(ctx, "zone resolved", ...)` line with `realm`, `realm_id`, `zonegroup`, `zonegroup_id`, `zone`, `zone_id`, `period`, `period_epoch`, `from_period` — the four lines radosgw prints at [`svc_zone.cc:271-276`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_zone.cc#L271-L276).

- [ ] **Step 5: Run the suite; expected PASS; run the whole gate**

```sh
go test -tags=ceph_preview ./internal/driver/ ./internal/testutil/fakerados/
make check
```

- [ ] **Step 6: Commit**

Two commits: `test(fakerados): add an in-memory seam cluster with class emulation for driver specs` and `feat(driver): resolve the realm, period, zonegroup and zone from the root pool`. Draft PR `feat(driver): zone resolution from the root pool (unit M task 1)`.

### Task 2: Control objects and the notify watch with bounded re-registration

**Files:**
- Create: `internal/driver/notify.go`, `internal/driver/notify_test.go`
- Modify: `internal/meta/bucket_layout.go` (export `StrHashLinux`), `internal/meta/bucket_test.go` (one pin), `docs/exclusions.md` (the cache-invalidation bullet gains rgw-go's handling)

**Interfaces:**
- Consumes: `radosclient.Pool` (`Write`, `Watch`, `Notify`), `radosclient.Watch`, `radosclient.ErrExists`, `radosclient.ErrTimedOut`, `meta.CacheNotifyInfo`, `meta.DecodeCacheNotifyInfo`, `meta.CacheUpdateObj`, `meta.CacheInvalidateObj`, `meta.RawObj`, `denc.NewEncoder`/`NewDecoder`, `golang.org/x/sync/errgroup`.
- Produces:

```go
package driver

// controlPrefix is notify_oid_prefix (svc_notify.cc:19).
const controlPrefix = "notify"

// controlOIDs is init_watch's naming (svc_notify.cc:199-221 at v19.2.6;
// :162-183 at v20.2.4): rgw_num_control_oids objects "notify.<i>"; 0 selects
// the single legacy object "notify"; a negative value one object "notify.0".
func controlOIDs(num int64) []string

// notifier is RGWSI_Notify: the control objects, one watch on each, the
// cache's enable flag, and the cache-notify record after every write.
type notifier struct{ /* pool, oids, release, maxRetries, handle, onEnabled, backoffMin, backoffMax, sleep, mu, up */ }
func newNotifier(pool radosclient.Pool, oids []string, release denc.Release, maxRetries uint64,
	handle func(meta.CacheNotifyInfo), onEnabled func(bool)) *notifier
func (n *notifier) createControlObjects(ctx context.Context) error
func (n *notifier) run(ctx context.Context) error            // registered with Store.AddWorker("control-watch", n.run)
func (n *notifier) pick(key string) string                   // pick_control_obj
func (n *notifier) distribute(ctx context.Context, key string, info meta.CacheNotifyInfo) error // robust_notify

// encodeAt renders a meta type at the release.
func encodeAt(v interface{ Encode(*denc.Encoder, denc.Release) }, r denc.Release) []byte
```

```go
package meta

// StrHashLinux is ceph_str_hash_linux (exported; was strHashLinux).
func StrHashLinux(s string) uint32
```

- [ ] **Step 1: Write the failing specs**

`internal/driver/notify_test.go`, package `driver` (white-box: the notifier is unexported and the suite bootstrap in `driver_test` collects it):

```go
package driver

var _ = Describe("controlOIDs", func() {
	It("names rgw_num_control_oids objects", func() {
		Expect(controlOIDs(8)).To(Equal([]string{"notify.0", "notify.1", "notify.2", "notify.3", "notify.4", "notify.5", "notify.6", "notify.7"}))
	})
	It("uses the legacy single object for zero and one object for a negative count", func() {
		Expect(controlOIDs(0)).To(Equal([]string{"notify"}), "compat_oid, svc_notify.cc:203")
		Expect(controlOIDs(-3)).To(Equal([]string{"notify.0"}), "num_watchers <= 0 becomes 1, svc_notify.cc:205")
	})
})

var _ = Describe("notifier", func() {
	var (
		c        *fakerados.Cluster
		pool     radosclient.Pool
		n        *notifier
		handled  []meta.CacheNotifyInfo
		enabled  []bool
		sleeps   []time.Duration
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		var err error
		pool, err = c.Pool(ctx, "zone.rgw.control", "")
		Expect(err).NotTo(HaveOccurred())
		handled, enabled, sleeps = nil, nil, nil
		n = newNotifier(pool, controlOIDs(8), denc.Squid, 10,
			func(i meta.CacheNotifyInfo) { handled = append(handled, i) },
			func(b bool) { enabled = append(enabled, b) })
		n.sleep = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	})
	It("picks the control object by ceph_str_hash_linux of the cache name", func() {
		Expect(meta.StrHashLinux("a")).To(BeEquivalentTo(17138), "(97<<4 + 97>>4) * 11")
		Expect(n.pick("a")).To(Equal("notify.2"), "17138 % 8")
	})
	It("creates every control object and leaves an existing one alone", func(ctx SpecContext) {
		c.Put("zone.rgw.control", "", "notify.3", []byte("x"))
		Expect(n.createControlObjects(ctx)).To(Succeed())
		for i := range 8 {
			Expect(c.Object("zone.rgw.control", "", fmt.Sprintf("notify.%d", i))).NotTo(BeNil(), "notify.%d", i)
		}
		Expect(c.Object("zone.rgw.control", "", "notify.3").Data).To(Equal([]byte("x")), "create(false) keeps the data")
	})
	It("distributes the record to the picked object as radosgw encodes it", func(ctx SpecContext) {
		info := meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("zone.rgw.meta:root"), OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("d")}}
		Expect(n.distribute(ctx, "a", info)).To(Succeed())
		got := c.Notifies("zone.rgw.control", "", "notify.2")
		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(encodeAt(info, denc.Squid)))
	})
	It("retries a timed-out notify with invalidations up to rgw_max_notify_retries", func(ctx SpecContext) {
		c.FailNotify("zone.rgw.control", "", "notify.2", 3, radosclient.ErrTimedOut)
		info := meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: meta.ParsePool("p"), OID: "o"}}
		Expect(n.distribute(ctx, "a", info)).To(Succeed(), "the fourth attempt succeeds")
		got := c.Notifies("zone.rgw.control", "", "notify.2")
		Expect(got).To(HaveLen(4))
		inv := meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: info.Obj}
		Expect(got[1]).To(Equal(encodeAt(inv, denc.Squid)), "svc_notify.cc:482-487: the retries are invalidations of the same object")
		n.maxRetries = 2
		c.FailNotify("zone.rgw.control", "", "notify.2", 5, radosclient.ErrTimedOut)
		Expect(n.distribute(ctx, "a", info)).To(MatchError(radosclient.ErrTimedOut), "gives up after maxRetries and reports it")
	})
	It("registers a watch on every object and enables the cache once all are up", func(ctx SpecContext) {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- n.run(runCtx) }()
		Eventually(func() int { return c.Watches("zone.rgw.control", "", "notify.7") }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1))
		Eventually(func() []bool { return enabled }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		cancel()
		Eventually(done).WithTimeout(time.Second).Should(Receive(Succeed()))
		Expect(c.Watches("zone.rgw.control", "", "notify.0")).To(BeZero(), "closed on exit")
	})
	It("disables the cache when a watch is lost and re-enables it when the watch is back", func(ctx SpecContext) {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = n.run(runCtx) }()
		Eventually(func() []bool { return enabled }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true}))
		c.BreakWatches("zone.rgw.control", "", "notify.5", errors.New("osd down"))
		Eventually(func() []bool { return enabled }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal([]bool{true, false, true}))
		Expect(sleeps).To(Equal([]time.Duration{time.Second}), "one backoff before re-registering")
	})
	It("retries past one hundred failures without stopping, backing off up to thirty seconds and resetting on success", func(ctx SpecContext) {
		c.FailWatch("zone.rgw.control", "", "notify.1", 120, errors.New("no osd"))
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = n.run(runCtx) }()
		Eventually(func() int { return c.Watches("zone.rgw.control", "", "notify.1") }).WithTimeout(2 * time.Second).WithPolling(time.Millisecond).Should(Equal(1))
		Expect(sleeps).To(HaveLen(120), "every failure slept once; the 101st was not fatal (tracker #80992)")
		Expect(sleeps[:6]).To(Equal([]time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}))
		Expect(sleeps[119]).To(Equal(30 * time.Second), "capped")
		c.BreakWatches("zone.rgw.control", "", "notify.1", errors.New("again"))
		Eventually(func() int { return len(sleeps) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(121))
		Expect(sleeps[120]).To(Equal(time.Second), "reset after the successful registration")
	})
	It("decodes a notify and hands it to the cache, ignoring one that does not decode", func(ctx SpecContext) {
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = n.run(runCtx) }()
		Eventually(func() int { return c.Watches("zone.rgw.control", "", "notify.2") }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1))
		info := meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: meta.ParsePool("p"), OID: "o"}}
		_, err := pool.Notify(ctx, "notify.2", encodeAt(info, denc.Squid), 0)
		Expect(err).NotTo(HaveOccurred())
		_, err = pool.Notify(ctx, "notify.2", []byte{1, 2, 3}, 0)
		Expect(err).NotTo(HaveOccurred(), "a bad payload is acked, not failed (svc_notify.cc:77-80)")
		Expect(handled).To(Equal([]meta.CacheNotifyInfo{info}))
	})
})
```

- [ ] **Step 2: Run to see it fail**

```sh
go test -tags=ceph_preview ./internal/driver/
```

Expected: undefined `controlOIDs`, `newNotifier`, `meta.StrHashLinux`.

- [ ] **Step 3: Export the hash and write `notify.go`**

In `internal/meta/bucket_layout.go` rename `strHashLinux` to `StrHashLinux`, keep the comment ("StrHashLinux is ceph_str_hash_linux."), update `IndexShard`; add to `bucket_test.go` one `It("hashes as ceph_str_hash_linux", func() { Expect(meta.StrHashLinux("a")).To(BeEquivalentTo(17138)) })`.

`internal/driver/notify.go`:

```go
package driver

const controlPrefix = "notify"

func controlOIDs(num int64) []string {
	if num == 0 {
		return []string{controlPrefix}
	}
	if num < 0 {
		num = 1
	}
	oids := make([]string, num)
	for i := range oids {
		oids[i] = controlPrefix + "." + strconv.Itoa(i)
	}
	return oids
}

const (
	watchBackoffMin = time.Second
	watchBackoffMax = 30 * time.Second
)

type notifier struct {
	pool       radosclient.Pool
	oids       []string
	release    denc.Release
	maxRetries uint64
	handle     func(meta.CacheNotifyInfo)
	onEnabled  func(bool)

	backoffMin, backoffMax time.Duration
	sleep                  func(context.Context, time.Duration) error

	mu sync.Mutex
	up map[int]bool
}

func newNotifier(pool radosclient.Pool, oids []string, release denc.Release, maxRetries uint64,
	handle func(meta.CacheNotifyInfo), onEnabled func(bool)) *notifier {
	return &notifier{pool: pool, oids: oids, release: release, maxRetries: maxRetries, handle: handle, onEnabled: onEnabled,
		backoffMin: watchBackoffMin, backoffMax: watchBackoffMax, sleep: sleepCtx, up: map[int]bool{}}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// createControlObjects is init_watch's op.create(false) on each control
// object (svc_notify.cc:231-238): it exists afterwards, whatever it held.
func (n *notifier) createControlObjects(ctx context.Context) error {
	for _, oid := range n.oids {
		wop := radosclient.NewWriteOp()
		wop.Create(false)
		if _, err := n.pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrExists) {
			return fmt.Errorf("creating control object %s: %w", oid, err)
		}
	}
	return nil
}

// run keeps one watch on every control object until ctx ends. radosgw
// re-registers at once and aborts the process after a lifetime of 100
// failures (svc_notify.cc:89-115, tracker #80992); this loop sleeps a
// doubling backoff between attempts, caps it, resets it on success and
// counts nothing.
func (n *notifier) run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)
	for i, oid := range n.oids {
		g.Go(func() error { return n.watchLoop(ctx, i, oid) })
	}
	return g.Wait()
}

func (n *notifier) watchLoop(ctx context.Context, i int, oid string) error {
	backoff := n.backoffMin
	for {
		w, err := n.pool.Watch(ctx, oid, n.onNotify)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			slog.ErrorContext(ctx, "control watch registration failed", slog.String("oid", oid), slog.Duration("retry_in", backoff), slog.Any("error", err))
			if n.sleep(ctx, backoff) != nil {
				return nil
			}
			backoff = min(backoff*2, n.backoffMax)
			continue
		}
		backoff = n.backoffMin
		n.setUp(i, true)
		select {
		case <-ctx.Done():
			n.setUp(i, false)
			return w.Close()
		case err := <-w.Err():
			slog.ErrorContext(ctx, "control watch lost", slog.String("oid", oid), slog.Duration("retry_in", backoff), slog.Any("error", err))
			n.setUp(i, false)
			if cerr := w.Close(); cerr != nil {
				slog.DebugContext(ctx, "closing a lost watch", slog.String("oid", oid), slog.Any("error", cerr))
			}
			if n.sleep(ctx, backoff) != nil {
				return nil
			}
			backoff = min(backoff*2, n.backoffMax)
		}
	}
}

// setUp is add_watcher/remove_watcher (svc_notify.cc:343-363): the cache is
// enabled exactly when every watch is registered.
func (n *notifier) setUp(i int, up bool) {
	n.mu.Lock()
	before := len(n.up) == len(n.oids)
	if up {
		n.up[i] = true
	} else {
		delete(n.up, i)
	}
	after := len(n.up) == len(n.oids)
	n.mu.Unlock()
	switch {
	case after && !before:
		slog.Info("all control watches registered, enabling the metadata cache", slog.Int("watches", len(n.oids)))
		n.onEnabled(true)
	case before && !after:
		slog.Warn("a control watch is down, disabling the metadata cache")
		n.onEnabled(false)
	}
}

// onNotify is RGWWatcher::handle_notify plus the decode in
// RGWSI_SysObj_Cache::watch_cb (svc_notify.cc:56-81, svc_sys_obj_cache.cc:465-482):
// a record that does not decode is logged and dropped; the seam acks either way.
func (n *notifier) onNotify(_, notifierID uint64, payload []byte) {
	d := denc.NewDecoder(payload)
	info := meta.DecodeCacheNotifyInfo(d)
	if err := d.Err(); err != nil {
		slog.Warn("bad cache notify", slog.Uint64("notifier_id", notifierID), slog.Any("error", err))
		return
	}
	n.handle(info)
}

// pick is pick_control_obj (svc_notify.cc:191-197).
func (n *notifier) pick(key string) string {
	return n.oids[meta.StrHashLinux(key)%uint32(len(n.oids))] //nolint:gosec // len(oids) is a small positive count
}

// distribute is RGWSI_Notify::distribute and robust_notify
// (svc_notify.cc:394-513): one notify with librados's default timeout, then,
// while it keeps timing out, up to rgw_max_notify_retries INVALIDATE_OBJ
// notifies for the same object. The caller logs a failure and carries on,
// as radosgw does.
func (n *notifier) distribute(ctx context.Context, key string, info meta.CacheNotifyInfo) error {
	oid := n.pick(key)
	_, err := n.pool.Notify(ctx, oid, encodeAt(info, n.release), 0)
	if !errors.Is(err, radosclient.ErrTimedOut) {
		return err
	}
	retry := encodeAt(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: info.Obj}, n.release)
	for tries := uint64(0); errors.Is(err, radosclient.ErrTimedOut) && tries < n.maxRetries; tries++ {
		slog.WarnContext(ctx, "cache notify timed out, sending an invalidation", slog.String("oid", oid), slog.String("obj", info.Obj.OID), slog.Uint64("try", tries))
		_, err = n.pool.Notify(ctx, oid, retry, 0)
	}
	return err
}

func encodeAt(v interface{ Encode(*denc.Encoder, denc.Release) }, r denc.Release) []byte {
	e := denc.NewEncoder()
	v.Encode(e, r)
	return e.Bytes()
}
```

- [ ] **Step 4: Run the suite; expected PASS**

```sh
go test -tags=ceph_preview -race ./internal/driver/ ./internal/meta/
```

- [ ] **Step 5: Record the handling in `docs/exclusions.md`**

In the "Metadata cache invalidation, both directions" bullet of the coexistence section, append: "rgw-go honours a notify by invalidating the named entry and re-reading it on the next access, whichever op the record carries, because radosgw applies an `UPDATE_OBJ` payload unchecked (`docs/ceph-upstream-bugs.md`); it sends `UPDATE_OBJ` with the written record after its own writes so that a coexisting radosgw need not re-read. Its control watches re-register with a backoff and never abort the process, and it keeps sending and watching notifies even when `rgw_cache_enabled` is false, which radosgw does not." Report the change for the rgw-rs session.

- [ ] **Step 6: Commit**

`feat(driver): create the control objects and keep the notify watches registered with backoff`; the meta export goes in the same commit. Draft PR `feat(driver): control objects and notify watches (unit M task 2)`.

### Task 3: The sysobj layer and the metadata cache with invalidate-on-notify

**Files:**
- Create: `internal/driver/cache.go`, `internal/driver/cache_test.go`, `internal/driver/objv.go`, `internal/driver/sysobj.go`, `internal/driver/sysobj_test.go`, `internal/testutil/fakerados/cls_version.go`, `internal/meta/objversion.go` (only if G's Task 1 did not add `meta.ObjVersion`; M-D9)
- Modify: `internal/driver/store.go` (`Open` builds the cache, the notifier and the sysobj layer; `AddWorker("control-watch", ...)`), `internal/meta/cachenotify.go` (`ObjectCacheInfo.Version` becomes `meta.ObjVersion` if it is still `version.ObjVersion`)

**Interfaces:**
- Consumes: Task 2's `notifier`, `meta.CacheFlag*`, `meta.ObjectCacheInfo`, `meta.ObjectMetaInfo`, `cls/version` (`Set`, `Inc`, `Check`, `Read`, `CondEQ`, `ObjVersion`, `XattrName`), `radosclient.ReadOp`/`WriteOp` steps, `radosclient.StepFlagFailOK`, `radosclient.ErrNotFound`, `radosclient.ErrCanceled`, `cephconf.Options` (`rgw_cache_enabled`, `rgw_cache_lru_size`, `rgw_cache_expiry_interval`, `rgw_num_control_oids`, `rgw_max_notify_retries`).
- Produces:

```go
package meta

// ObjVersion is cls_version's obj_version as meta stores it; it converts to
// and from version.ObjVersion by struct conversion.
type ObjVersion struct {
	Ver uint64 `json:"ver"`
	Tag string `json:"tag"`
}
```

Add the `meta` declarations only when G has not already added them (M-D9).

```go
package driver

// normalName is RGWSI_SysObj_Cache's normal_name: "<pool>+<ns>+<oid>"
// (svc_sys_obj_cache.cc:67-72). It keys the cache and picks the control object.
func normalName(p meta.Pool, oid string) string

// cacheInfo is ObjectCacheInfo without the wire form.
type cacheInfo struct {
	status  int32 // 0, or -ENOENT for a negative entry
	flags   uint32
	data    []byte
	xattrs  map[string][]byte
	size    uint64
	mtime   time.Time
	version meta.ObjVersion
}

// objectCache is ObjectCache (rgw_cache.cc): an LRU of rgw_cache_lru_size
// entries expiring rgw_cache_expiry_interval after they were added, with
// negative entries and per-flag partial hits, enabled only while every
// control watch is registered.
type objectCache struct{ /* mu, enabled, entries, lru, maxSize, expiry, now, domainRoot */ }
func newObjectCache(maxSize int, expiry time.Duration, domainRoot meta.Pool, now func() time.Time) *objectCache
var errNegativeEntry = errors.New("driver: cached negative entry")
func (c *objectCache) get(name string, mask uint32) (cacheInfo, error) // radosclient.ErrNotFound-wrapping errNegativeEntry for a negative hit, errCacheMiss otherwise
func (c *objectCache) put(name string, info cacheInfo)
func (c *objectCache) invalidateRemove(name string) bool
func (c *objectCache) setEnabled(on bool)
func (c *objectCache) onNotify(info meta.CacheNotifyInfo) // invalidates on every notify, UPDATE_OBJ included

// objv is RGWObjVersionTracker (rgw_rados.cc:158-197).
type objv struct{ read, write meta.ObjVersion }
func newWriteVersion() meta.ObjVersion                       // generate_new_write_ver: Ver 1, 24 random alphanumerics
func (v *objv) prepareRead(rop *radosclient.ReadOp, r denc.Release) *version.ReadResult
func (v *objv) prepareWrite(wop *radosclient.WriteOp, r denc.Release)
func (v *objv) applyWrite()

// sysObj names a metadata object.
type sysObj struct {
	pool meta.Pool
	oid  string
}

// sysobjs is RGWSI_SysObj_Cache over RGWSI_SysObj_Core.
type sysobjs struct{ /* pools *poolCache, cache *objectCache, notify *notifier, release, now */ }
type readParams struct {
	data, attrs, meta bool
	objv              *objv
}
type readResult struct {
	data    []byte
	attrs   map[string][]byte
	size    uint64
	mtime   time.Time
	version meta.ObjVersion
}
func (s *sysobjs) read(ctx context.Context, o sysObj, p readParams) (readResult, error)
func (s *sysobjs) write(ctx context.Context, o sysObj, data []byte, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) (time.Time, error)
func (s *sysobjs) setAttrs(ctx context.Context, o sysObj, set map[string][]byte, rm []string, exclusive bool, v *objv) error
func (s *sysobjs) remove(ctx context.Context, o sysObj, v *objv) error
```

```go
package fakerados

// VersionClass emulates cls_version: set, inc, inc_conds, read and
// check_conds over the xattr ceph.objclass.version, with ECANCELED for a
// failed condition (cls_version.cc:166, :196; docs/ceph-upstream-bugs.md).
func VersionClass() ClassFunc
```

Semantics, each from the C++ cited: `get` misses when disabled, when absent, when expired (`now - timeAdded > expiry`, erasing the entry), or when `flags & mask != mask` (a type miss); a negative entry hits with `errNegativeEntry` ([rgw_cache.cc:13-96](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_cache.cc#L13-L96)). `put` resets `timeAdded`, moves the entry to the LRU back, evicts from the LRU front while the cache is over `maxSize`; a negative `status` clears flags, xattrs and data; otherwise flags are or-ed, `META` replaces size and mtime while a non-meta change without `MODIFY_XATTRS` drops the META flag, `XATTRS` replaces the set, `MODIFY_XATTRS` erases then overlays, `DATA` replaces the data, `OBJV` replaces the version ([:142-215](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_cache.cc#L142-L215)). `invalidateRemove` never caches a negative ([:217-241](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_cache.cc#L217-L241)). `setEnabled(false)` flushes everything ([:303-312](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_cache.cc#L303-L312)). `onNotify` normalises the object (an empty oid means the domain-root pool and the source pool's name as oid, [svc_sys_obj_cache.cc:74-83](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L74-L83)) and invalidates, for `UPDATE_OBJ` and `INVALIDATE_OBJ` alike.

`sysobjs.read` composes one `ReadOp`: `prepareRead` when a tracker is given (a `check_conds EQ` when `read.Ver != 0`, then `read`), `Read(0, rootObjectMax)` when data is wanted, `Stat` when size or mtime are, `GetXattrs` when attrs are (svc_sys_obj_core.cc read, and `RGWSI_SysObj_Cache::read` [:113-240](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L113-L240) for the flag set); a hit serves everything from the entry; `ENOENT` caches a negative entry ([:200-205](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L200-L205)); a successful read caches data (unless it filled the buffer), attrs (unfiltered), meta and version. `write` composes `Create(true)` when exclusive, else `Remove(); SetStepFlags(FailOK); Create(false)` ([svc_sys_obj_core.cc:496-502](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L496-L502)), then `prepareWrite`, `SetMtime(mtime or now)`, `WriteFull(data)`, `SetXattr` for every non-empty attr ([:504-526](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L504-L526)), runs it, `applyWrite`, puts `XATTRS|DATA|META|OBJV` locally and distributes `UPDATE_OBJ` with the record ([svc_sys_obj_cache.cc:312-354](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L312-L354)); on error it invalidates the entry. `setAttrs` composes `Create(true)` when exclusive, `prepareWrite`, `RmXattr` each, `SetXattr` each non-empty, skips an empty op, puts `MODIFY_XATTRS` and distributes `UPDATE_OBJ` ([:277-310](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L277-L310), [svc_sys_obj_core.cc:237-291](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_core.cc#L237-L291)). `remove` composes `prepareWrite` and `Remove`, invalidates and distributes `INVALIDATE_OBJ` ([:86-111](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L86-L111), [:451-475](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L451-L475)). A distribute failure is logged at error level and does not fail the call ([:105-108](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L105-L108), [:302-304](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L302-L304), [:346-348](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_sys_obj_cache.cc#L346-L348)). A version check that fails surfaces as `radosclient.ErrCanceled`, which Task 5 maps to `op.ErrConcurrentModification`.

- [ ] **Step 1: Add `meta.ObjVersion` if G left it out**

```sh
grep -n 'type ObjVersion' internal/meta/*.go || echo missing
```

If missing, create `internal/meta/objversion.go` with the type above plus `Encode(e, _ denc.Release)` (`BeginStruct(1, 1)`, `U64`, `String`) and `DecodeObjVersion`, switch `cachenotify.go` to it (`c.Version.Encode`, `DecodeObjVersion`) and drop its `cls/version` import; run `go test -tags=ceph_preview ./internal/meta/` — the cache-notify goldens must still pass byte for byte.

- [ ] **Step 2: Write the failing cache specs**

`internal/driver/cache_test.go`, package `driver`:

```go
var _ = Describe("objectCache", func() {
	var (
		now   time.Time
		c     *objectCache
		root  = meta.ParsePool("zone.rgw.meta:root")
		name  = normalName(root, "plain")
	)
	BeforeEach(func() {
		now = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
		c = newObjectCache(3, 900*time.Second, root, func() time.Time { return now })
		c.setEnabled(true)
	})
	It("keys entries by pool, namespace and oid", func() {
		Expect(name).To(Equal("zone.rgw.meta+root+plain"))
	})
	It("misses while disabled, and flushes when disabled", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.setEnabled(false)
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
		c.setEnabled(true)
		_, err = c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "the flush is not undone by enabling")
	})
	It("serves only the flags it holds", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagXattrs, xattrs: map[string][]byte{"user.rgw.acl": {1}}})
		_, err := c.get(name, meta.CacheFlagXattrs|meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "type miss, rgw_cache.cc:77-83")
		got, err := c.get(name, meta.CacheFlagXattrs)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.xattrs).To(HaveKey("user.rgw.acl"))
	})
	It("returns a negative entry as not found", func() {
		c.put(name, cacheInfo{status: -int32(syscall.ENOENT)})
		_, err := c.get(name, meta.CacheFlagXattrs)
		Expect(err).To(MatchError(errNegativeEntry))
		Expect(err).To(MatchError(radosclient.ErrNotFound), "callers see the seam's sentinel")
	})
	It("expires an entry after rgw_cache_expiry_interval", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		now = now.Add(901 * time.Second)
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
	})
	It("evicts the least recently used entry past rgw_cache_lru_size", func() {
		for _, k := range []string{"a", "b", "c"} {
			c.put(normalName(root, k), cacheInfo{flags: meta.CacheFlagData, data: []byte(k)})
		}
		_, err := c.get(normalName(root, "a"), meta.CacheFlagData) // a is now the most recent
		Expect(err).NotTo(HaveOccurred())
		c.put(normalName(root, "d"), cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		_, err = c.get(normalName(root, "b"), meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "b was the oldest")
		_, err = c.get(normalName(root, "a"), meta.CacheFlagData)
		Expect(err).NotTo(HaveOccurred())
	})
	It("merges a modify-xattrs put over the stored set", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagXattrs | meta.CacheFlagMeta, xattrs: map[string][]byte{"a": {1}, "b": {2}}, size: 9})
		c.put(name, cacheInfo{flags: meta.CacheFlagModifyXattrs, xattrs: map[string][]byte{"c": {3}}, rmxattrs: map[string][]byte{"a": nil}})
		got, err := c.get(name, meta.CacheFlagXattrs|meta.CacheFlagMeta)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.xattrs).To(Equal(map[string][]byte{"b": {2}, "c": {3}}), "rgw_cache.cc:198-208")
		Expect(got.size).To(BeEquivalentTo(9), "META survives a modify-xattrs put, :187-190")
	})
	It("never stores a notify payload: both ops invalidate and nothing is created", func() {
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("stale")})
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("forged")}})
		_, err := c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "UPDATE_OBJ removed the entry rather than replacing it")
		other := normalName(root, "absent")
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "absent"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("planted")}})
		_, err = c.get(other, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "no entry planted from a payload")
		c.put(name, cacheInfo{flags: meta.CacheFlagData, data: []byte("d")})
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: root, OID: "plain"}})
		_, err = c.get(name, meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss))
	})
	It("normalises a record without an oid onto the domain root", func() {
		c.put(normalName(root, "zone.rgw.meta:users.uid"), cacheInfo{flags: meta.CacheFlagData})
		c.onNotify(meta.CacheNotifyInfo{Op: meta.CacheInvalidateObj, Obj: meta.RawObj{Pool: meta.ParsePool("zone.rgw.meta:users.uid")}})
		_, err := c.get(normalName(root, "zone.rgw.meta:users.uid"), meta.CacheFlagData)
		Expect(err).To(MatchError(errCacheMiss), "svc_sys_obj_cache.cc:74-83")
	})
})
```

Add `rmxattrs map[string][]byte` to `cacheInfo` (the `rm_xattrs` of a modify put).

- [ ] **Step 3: Write the failing sysobj specs**

`internal/driver/sysobj_test.go`, package `driver`, on `fakerados` with `VersionClass()` registered and Task 2's notifier running against the fake control pool:

```go
var _ = Describe("sysobjs", func() {
	var (
		c      *fakerados.Cluster
		s      *sysobjs
		cache  *objectCache
		cancel context.CancelFunc
		root   = meta.ParsePool("zone.rgw.meta:root")
		obj    = sysObj{pool: root, oid: "plain"}
		now    = time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.RegisterClass("version", fakerados.VersionClass())
		pools := newPoolCache(c)
		control, err := pools.get(ctx, meta.ParsePool("zone.rgw.control"))
		Expect(err).NotTo(HaveOccurred())
		cache = newObjectCache(25000, 900*time.Second, root, func() time.Time { return now })
		n := newNotifier(control, controlOIDs(8), denc.Squid, 10, cache.onNotify, cache.setEnabled)
		Expect(n.createControlObjects(ctx)).To(Succeed())
		var runCtx context.Context
		runCtx, cancel = context.WithCancel(ctx)
		go func() { _ = n.run(runCtx) }()
		Eventually(func() int { return c.Watches("zone.rgw.control", "", "notify.7") }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(1))
		s = &sysobjs{pools: pools, cache: cache, notify: n, release: denc.Squid, now: func() time.Time { return now }}
	})
	AfterEach(func() { cancel() })

	It("writes exclusively with a fresh version, caches the record and distributes it", func(ctx SpecContext) {
		v := &objv{write: meta.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}}
		mt, err := s.write(ctx, obj, []byte("data"), map[string][]byte{"user.rgw.acl": {7}, "empty": {}}, true, time.Time{}, v)
		Expect(err).NotTo(HaveOccurred())
		Expect(mt).To(Equal(now), "mtime defaults to now, svc_sys_obj_core.cc:508-510")
		stored := c.Object("zone.rgw.meta", "root", "plain")
		Expect(stored.Data).To(Equal([]byte("data")))
		Expect(stored.Xattrs).To(HaveKey("user.rgw.acl"))
		Expect(stored.Xattrs).NotTo(HaveKey("empty"), "empty attrs are skipped, :522-523")
		d := denc.NewDecoder(stored.Xattrs[version.XattrName])
		Expect(version.DecodeObjVersion(d)).To(Equal(version.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}))
		Expect(v.read).To(Equal(meta.ObjVersion{Ver: 1, Tag: "tagtagtagtagtagtagtagtag"}), "apply_write, rgw_rados.cc:185-197")
		got, err := cache.get(normalName(root, "plain"), meta.CacheFlagData|meta.CacheFlagXattrs|meta.CacheFlagMeta|meta.CacheFlagObjVersion)
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("data")))
		payloads := c.Notifies("zone.rgw.control", "", s.notify.pick(normalName(root, "plain")))
		Expect(payloads).To(HaveLen(1))
		info := meta.DecodeCacheNotifyInfo(denc.NewDecoder(payloads[0]))
		Expect(info.Op).To(Equal(meta.CacheUpdateObj))
		Expect(info.Obj).To(Equal(meta.RawObj{Pool: root, OID: "plain"}))
		Expect(info.ObjInfo.Data).To(Equal([]byte("data")))
		Expect(info.ObjInfo.Flags).To(Equal(meta.CacheFlagXattrs | meta.CacheFlagData | meta.CacheFlagMeta | meta.CacheFlagObjVersion))
	})
	It("fails an exclusive write of an existing object with EEXIST and invalidates the entry", func(ctx SpecContext) {
		c.Put("zone.rgw.meta", "root", "plain", []byte("old"))
		_, err := s.write(ctx, obj, []byte("new"), nil, true, now, &objv{write: newWriteVersion()})
		Expect(err).To(MatchError(radosclient.ErrExists))
		Expect(c.Object("zone.rgw.meta", "root", "plain").Data).To(Equal([]byte("old")))
	})
	It("checks the read version on a versioned overwrite and reports a race as ECANCELED", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("v1"), nil, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		stale := &objv{read: meta.ObjVersion{Ver: 1, Tag: "someone-elses"}}
		_, err = s.write(ctx, obj, []byte("v2"), nil, false, now, stale)
		Expect(err).To(MatchError(radosclient.ErrCanceled), "cls_version check_conds EQ fails, cls_version.cc:196")
		fresh := &objv{read: v.read}
		_, err = s.write(ctx, obj, []byte("v2"), nil, false, now, fresh)
		Expect(err).NotTo(HaveOccurred())
		Expect(fresh.read.Ver).To(BeEquivalentTo(2), "inc applied locally")
		Expect(c.Object("zone.rgw.meta", "root", "plain").Data).To(Equal([]byte("v2")))
	})
	It("reads through the cache, caches a negative lookup and serves the remembered record", func(ctx SpecContext) {
		_, err := s.read(ctx, obj, readParams{data: true, attrs: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
		c.Put("zone.rgw.meta", "root", "plain", []byte("late"))
		_, err = s.read(ctx, obj, readParams{data: true, attrs: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound), "the negative entry is served until invalidated, svc_sys_obj_cache.cc:200-205")
		cache.invalidateRemove(normalName(root, "plain"))
		got, err := s.read(ctx, obj, readParams{data: true, attrs: true, meta: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("late")))
		Expect(got.size).To(BeEquivalentTo(4))
	})
	It("re-reads after another gateway's notify, whatever payload it carried", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("mine"), nil, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		// another gateway overwrites the object and announces a forged record
		c.Object("zone.rgw.meta", "root", "plain").Data = []byte("theirs")
		control, _ := s.pools.get(ctx, meta.ParsePool("zone.rgw.control"))
		forged := meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: root, OID: "plain"},
			ObjInfo: meta.ObjectCacheInfo{Flags: meta.CacheFlagData, Data: []byte("forged")}}
		_, err = control.Notify(ctx, s.notify.pick(normalName(root, "plain")), encodeAt(forged, denc.Squid), 0)
		Expect(err).NotTo(HaveOccurred())
		got, err := s.read(ctx, obj, readParams{data: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(got.data).To(Equal([]byte("theirs")), "re-read from RADOS, not the payload")
	})
	It("sets attrs with a modify record and removes with an invalidation", func(ctx SpecContext) {
		v := &objv{write: newWriteVersion()}
		_, err := s.write(ctx, obj, []byte("d"), map[string][]byte{"a": {1}}, true, now, v)
		Expect(err).NotTo(HaveOccurred())
		Expect(s.setAttrs(ctx, obj, map[string][]byte{"b": {2}}, []string{"a"}, false, &objv{read: v.read})).To(Succeed())
		Expect(c.Object("zone.rgw.meta", "root", "plain").Xattrs).To(HaveKey("b"))
		Expect(c.Object("zone.rgw.meta", "root", "plain").Xattrs).NotTo(HaveKey("a"))
		oid := s.notify.pick(normalName(root, "plain"))
		payloads := c.Notifies("zone.rgw.control", "", oid)
		last := meta.DecodeCacheNotifyInfo(denc.NewDecoder(payloads[len(payloads)-1]))
		Expect(last.ObjInfo.Flags & meta.CacheFlagModifyXattrs).NotTo(BeZero())
		Expect(s.remove(ctx, obj, nil)).To(Succeed())
		Expect(c.Object("zone.rgw.meta", "root", "plain")).To(BeNil())
		payloads = c.Notifies("zone.rgw.control", "", oid)
		Expect(meta.DecodeCacheNotifyInfo(denc.NewDecoder(payloads[len(payloads)-1])).Op).To(Equal(meta.CacheInvalidateObj))
		_, err = s.read(ctx, obj, readParams{data: true})
		Expect(err).To(MatchError(radosclient.ErrNotFound))
	})
	It("logs and carries on when the notify cannot be delivered", func(ctx SpecContext) {
		c.FailNotify("zone.rgw.control", "", s.notify.pick(normalName(root, "plain")), 20, errors.New("no watchers"))
		_, err := s.write(ctx, obj, []byte("d"), nil, true, now, &objv{write: newWriteVersion()})
		Expect(err).NotTo(HaveOccurred(), "svc_sys_obj_cache.cc:346-348: not fatal")
	})
})
```

- [ ] **Step 4: Run to see them fail; write `objv.go`, `cache.go`, `sysobj.go`, `fakerados.VersionClass`**

`objv.go`:

```go
package driver

const writeTagLen = 24 // RGWObjVersionTracker::generate_new_write_ver's TAG_LEN

// newWriteVersion is generate_new_write_ver: version 1 with a random tag.
func newWriteVersion() meta.ObjVersion { return meta.ObjVersion{Ver: 1, Tag: randAlnum(writeTagLen)} }

type objv struct{ read, write meta.ObjVersion }

func toCls(v meta.ObjVersion) version.ObjVersion   { return version.ObjVersion(v) }
func fromCls(v version.ObjVersion) meta.ObjVersion { return meta.ObjVersion(v) }

// prepareRead is prepare_op_for_read (rgw_rados.cc:158-167): check EQ when
// a read version is known, then read the stored version.
func (v *objv) prepareRead(rop *radosclient.ReadOp, r denc.Release) *version.ReadResult {
	if v.read.Ver != 0 {
		version.Check(rop, toCls(v.read), version.CondEQ, r)
	}
	return version.Read(rop, r)
}

// prepareWrite is prepare_op_for_write (:169-183).
func (v *objv) prepareWrite(wop *radosclient.WriteOp, r denc.Release) {
	if v.read.Ver != 0 {
		version.Check(wop, toCls(v.read), version.CondEQ, r)
	}
	if v.write.Ver != 0 {
		version.Set(wop, toCls(v.write), r)
	} else {
		version.Inc(wop, r)
	}
}

// applyWrite is apply_write (:185-197).
func (v *objv) applyWrite() {
	checked, incremented := v.read.Ver != 0, v.write.Ver == 0
	if checked && incremented {
		v.read.Ver++
	} else {
		v.read = v.write
	}
	v.write = meta.ObjVersion{}
}
```

`randAlnum(n)` draws from `crypto/rand` over `[0-9A-Za-z]`, as `gen_rand_alphanumeric` does; a `randAlnum` may already exist in `memstore` (G's bucket ids) — reuse only if it is exported, else keep this private copy.

`cache.go` implements the semantics stated under Interfaces with a `container/list` LRU and `sync.RWMutex`; `get` upgrades to a write lock to expire or promote, as `ObjectCache::get` does; `errCacheMiss` is a package sentinel; a negative hit returns `fmt.Errorf("%w: %w", errNegativeEntry, radosclient.ErrNotFound)`.

`sysobj.go` implements `read`, `write`, `setAttrs`, `remove` as stated; the read path:

```go
func (s *sysobjs) read(ctx context.Context, o sysObj, p readParams) (readResult, error) {
	name := normalName(o.pool, o.oid)
	var flags uint32
	if p.data { flags |= meta.CacheFlagData }
	if p.attrs { flags |= meta.CacheFlagXattrs }
	if p.meta { flags |= meta.CacheFlagMeta }
	if p.objv != nil { flags |= meta.CacheFlagObjVersion }
	if hit, err := s.cache.get(name, flags); err == nil {
		if p.objv != nil { p.objv.read = hit.version }
		return readResult{data: hit.data, attrs: hit.xattrs, size: hit.size, mtime: hit.mtime, version: hit.version}, nil
	} else if errors.Is(err, errNegativeEntry) {
		return readResult{}, fmt.Errorf("%s/%s: %w", o.pool, o.oid, radosclient.ErrNotFound)
	}
	pool, err := s.pools.get(ctx, o.pool)
	if err != nil { return readResult{}, err }
	rop := radosclient.NewReadOp()
	var ver *version.ReadResult
	if p.objv != nil { ver = p.objv.prepareRead(rop, s.release) }
	var data *radosclient.ReadResult
	if p.data { data = rop.Read(0, rootObjectMax) }
	stat := rop.Stat()            // always: the cache wants META (svc_sys_obj_cache.cc:179-191)
	xattrs := rop.GetXattrs()     // always: the cache stores the unfiltered set (:193-197)
	if _, err := pool.Read(ctx, o.oid, rop, radosclient.OpFlagNone); err != nil {
		if errors.Is(err, radosclient.ErrNotFound) {
			s.cache.put(name, cacheInfo{status: -int32(syscall.ENOENT)})
		}
		return readResult{}, fmt.Errorf("reading %s/%s: %w", o.pool, o.oid, err)
	}
	res := readResult{attrs: xattrs.Xattrs, size: stat.Size, mtime: stat.ModTime}
	info := cacheInfo{flags: meta.CacheFlagXattrs | meta.CacheFlagMeta, xattrs: xattrs.Xattrs, size: stat.Size, mtime: stat.ModTime}
	if data != nil {
		res.data = data.Data[:data.N]
		if data.N < rootObjectMax { info.flags |= meta.CacheFlagData; info.data = res.data } // a full buffer may be a partial object, :208-211
	}
	if ver != nil {
		v, err := ver.Version()
		if err != nil { return readResult{}, err }
		res.version = fromCls(v)
		p.objv.read = res.version
		info.flags |= meta.CacheFlagObjVersion
		info.version = res.version
	}
	s.cache.put(name, info)
	return res, nil
}
```

The write, setAttrs and remove bodies follow the composition stated above; each ends with

```go
	s.cache.put(name, info)
	if err := s.notify.distribute(ctx, name, meta.CacheNotifyInfo{Op: meta.CacheUpdateObj, Obj: meta.RawObj{Pool: o.pool, OID: o.oid}, ObjInfo: info.wire()}); err != nil {
		slog.ErrorContext(ctx, "failed to distribute cache", slog.String("pool", o.pool.String()), slog.String("oid", o.oid), slog.Any("error", err))
	}
```

where `cacheInfo.wire()` builds the `meta.ObjectCacheInfo` (status, flags, data, xattrs, rm_xattrs, meta, version). The remove path distributes `CacheInvalidateObj` with an empty `ObjInfo`.

`fakerados/cls_version.go`: decode the op with `version.DecodeSetOp`, `DecodeIncOp`, `DecodeCheckOp`; the stored version is the xattr `version.XattrName` (absent → zero); `set` stores; `inc` and `inc_conds` initialise `{Ver: 1, Tag: <8 random alphanumerics>}` when absent then increment after the conditions pass; `check_conds` evaluates each condition (`EQ` both fields, `GT/GE/LT/LE` on `Ver`, `TagEQ/TagNE` on `Tag`) and returns `-ECANCELED` on the first failure; `read` returns `version.ReadRet{Objv}` encoded; the created flag is true for `set`/`inc` on an absent object.

- [ ] **Step 5: Wire `Open`**

`Open` reads `rgw_cache_enabled`, `rgw_cache_lru_size`, `rgw_cache_expiry_interval`, `rgw_num_control_oids`, `rgw_max_notify_retries`; builds `newObjectCache(lru, expiry, params.DomainRoot, time.Now)`; opens the control pool (`params.ControlPool`) and the notifier with `controlOIDs(num)`; `createControlObjects` (a failure fails `Open`, as `init_watch` fails `do_start`); registers `AddWorker("control-watch", n.run)`; the cache's `setEnabled` is wrapped so that `rgw_cache_enabled=false` never enables it while the notifier still runs (Task 2 Step 5 records the difference). `s.sysobj = &sysobjs{...}`.

- [ ] **Step 6: Run, gate, commit**

```sh
go test -tags=ceph_preview -race ./internal/driver/ ./internal/meta/ ./internal/testutil/fakerados/
make check
```

`feat(driver): add the versioned sysobj layer and the metadata cache with invalidate-on-notify`. Draft PR `feat(driver): metadata cache and sysobj layer (unit M task 3)`.

### Task 4: User index lookups: uid, access key, email; PutUser, RemoveUser

**Files:**
- Create: `internal/driver/user.go`, `internal/driver/user_test.go`
- Modify: `internal/driver/store.go` (drop the `ErrNotImplemented` stubs these replace)

**Interfaces:**
- Consumes: Task 3's `sysobjs`, `objv`, `newWriteVersion`; `meta.UserObject`, `meta.DecodeUserObject`, `meta.UID`, `meta.DecodeUID`, `meta.UserID`, `meta.ParseUserID`, `meta.AccountInfo`, `meta.DecodeAccountInfo`, `meta.AttrPrefix`; `op.UserRecord`, `op.PutUserOptions`, `op.ErrNoSuchUser`, `op.ErrUserAlreadyExists`, `op.ErrConcurrentModification`, `op.ErrKeyExists`, `op.ErrInternalError`, `op.FromRADOS`, `op.ScopeUser`, `op.AnonymousUserID`.
- Produces:

```go
package driver

// The objects RGWSI_User_RADOS uses (svc_user_rados.cc at v19.2.6).
func (s *Store) userObj(id meta.UserID) sysObj        // users.uid pool, oid = id.String()             (:642)
func (s *Store) keyIndexObj(key string) sysObj        // users.keys pool, oid = the access key id     (:335, :517)
func (s *Store) emailIndexObj(email string) sysObj    // users.email pool, oid = lower-cased email     (:319-321, :529-531)
func (s *Store) swiftIndexObj(name string) sysObj     // users.swift pool, oid = the swift name        (:347, :540)
func (s *Store) ownerBucketsObj(owner meta.Owner) sysObj // user: users.uid pool, "<uid>.buckets" (:109-113); account: account pool, "buckets.<id>" (account.cc:44-50)
func (s *Store) accountObj(id string) sysObj          // account pool, "account.<id>"                   (account.cc:84-90)

func (s *Store) readAccount(ctx context.Context, id string) (meta.AccountInfo, error)

// mapUserErr maps a seam error from a user object: ECANCELED is a lost
// version race, everything else is op.FromRADOS at user scope.
func mapUserErr(err error) error

// filterRGWAttrs keeps the user.rgw.* attrs, as rgw_filter_attrset does.
func filterRGWAttrs(attrs map[string][]byte) map[string][]byte

// The op.UserStore methods except ListUserBuckets.
func (s *Store) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error)
func (s *Store) GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error)
func (s *Store) GetUserByEmail(ctx context.Context, email string) (*op.UserRecord, error)
func (s *Store) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error
func (s *Store) RemoveUser(ctx context.Context, rec *op.UserRecord) error
```

Semantics, from `svc_user_rados.cc` at v19.2.6:

- `GetUser` (`read_user_info`, [:115-156](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L115-L156)): the anonymous id is `ErrNoSuchUser` without a read ([:125-128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L125-L128)); the uid object holds `RGWUID` then `RGWUserInfo` (`meta.DecodeUserObject`); a UID that is not the requested user is `ErrInternalError` wrapping a descriptive error (radosgw's `-EIO`, [:143-146](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L143-L146)); `Attrs` are the `user.rgw.*` xattrs; `Version` is the tracker's read version; `Mtime` the object's.
- `GetUserByAccessKey`/`GetUserByEmail` (`get_user_info_from_index`, [:650-690](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L650-L690)): read the index object, decode `RGWUID`, then `GetUser`; the email index is looked up lower-cased; a missing index is `ErrNoSuchUser`.
- `PutUser` (`store_user_info` → `PutOperation`, [:190-511](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L190-L511)): with `IfVersion` the write version is the read version plus one under the same tag, otherwise a fresh version ([:234-241](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L234-L241)); the current record is read first when not exclusive, as `old_info`; an active access key already mapped to another user is `ErrKeyExists` ([:257-270](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L257-L270), radosgw's `-EEXIST`); the data is `encode(RGWUID) + encode(RGWUserInfo)` with the record's attrs, its mtime or now, exclusive when asked ([:295-307](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L295-L307)), `EEXIST` → `ErrUserAlreadyExists`, `ECANCELED` → `ErrConcurrentModification`; then the email index (when the email is new or changed, case-insensitively), the key indexes of newly active keys and the swift indexes, each written non-versioned with the encoded `RGWUID` as data and the same `exclusive` ([:309-351](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L309-L351)); then the stale indexes of the old record are removed ([:399-443](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L399-L443)); `ENOENT` on a removal is ignored. The account and group user indexes (:360-394) are unit N's admin surface and are not written here; a stored `AccountID` is preserved as data.
- `RemoveUser` (`remove_user_info`, [:551-630](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L551-L630)): remove every active key index, swift index and the email index (ENOENT ignored), the `<uid>.buckets` object when the user has no account ([:592-600](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L592-L600)), then the uid object with the record's version as the check ([:624-627](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L624-L627)); `ECANCELED` → `ErrConcurrentModification`, `ENOENT` on the uid object → `ErrNoSuchUser`.
- `readAccount` reads `account.<id>` and decodes `meta.AccountInfo`; Task 6 uses its tenant, Task 9 its quotas.

- [ ] **Step 1: Write the failing specs**

`internal/driver/user_test.go`, package `driver_test`, on a store opened as in Task 1 (`seedRookZone` + `driver.Open`) over a `fakerados.Cluster` with `VersionClass` registered; a helper `seedUser(c, params, info)` writes the uid object as radosgw does (`encode(meta.UserObject{UID: meta.UID(info.UserID.String()), Info: info})` plus the version xattr) and the key and email index objects (`encode(meta.UID(...))`):

```go
var _ = Describe("UserStore", func() {
	var (
		c     *fakerados.Cluster
		s     *driver.Store
		uidNS = "ceph-objectstore.rgw.meta"
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass())
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
	})
	alice := func() meta.UserInfo {
		u := meta.NewUserInfo()
		u.UserID = meta.UserID{ID: "alice"}
		u.DisplayName, u.Email = "Alice", "Alice@Example.com"
		u.AccessKeys = map[string]meta.AccessKey{"AKALICE": {ID: "AKALICE", Secret: "s", Active: true}}
		return u
	}
	It("reads a radosgw-written user with its rgw attrs and version", func(ctx SpecContext) {
		seedUser(c, alice(), map[string][]byte{"user.rgw.iam-policy": []byte("{}"), "ceph.other": {1}}, meta.ObjVersion{Ver: 3, Tag: "t"})
		rec, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.DisplayName).To(Equal("Alice"))
		Expect(rec.Attrs).To(HaveKey("user.rgw.iam-policy"))
		Expect(rec.Attrs).NotTo(HaveKey("ceph.other"), "rgw_filter_attrset keeps user.rgw.*")
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 3, Tag: "t"}))
	})
	It("is NoSuchUser for a missing or anonymous user", func(ctx SpecContext) {
		_, err := s.GetUser(ctx, meta.UserID{ID: "nobody"})
		Expect(err).To(MatchError(op.ErrNoSuchUser))
		_, err = s.GetUser(ctx, meta.UserID{ID: op.AnonymousUserID})
		Expect(err).To(MatchError(op.ErrNoSuchUser))
		Expect(c.Reads(uidNS, "users.uid", op.AnonymousUserID)).To(BeZero(), "svc_user_rados.cc:125-128")
	})
	It("rejects a uid object that names another user", func(ctx SpecContext) {
		bob := alice()
		bob.UserID = meta.UserID{ID: "bob"}
		c.Put(uidNS, "users.uid", "alice", encode(meta.UserObject{UID: "bob", Info: bob}))
		_, err := s.GetUser(ctx, meta.UserID{ID: "alice"})
		Expect(err).To(MatchError(op.ErrInternalError))
	})
	It("resolves an access key and a mixed-case email through the index objects", func(ctx SpecContext) {
		seedUser(c, alice(), nil, meta.ObjVersion{Ver: 1, Tag: "t"})
		rec, err := s.GetUserByAccessKey(ctx, "AKALICE")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.UserID.ID).To(Equal("alice"))
		rec, err = s.GetUserByEmail(ctx, "ALICE@example.COM")
		Expect(err).NotTo(HaveOccurred(), "the index is lower-cased on both sides, :319-321")
		Expect(rec.Info.UserID.ID).To(Equal("alice"))
		_, err = s.GetUserByAccessKey(ctx, "AKNOBODY")
		Expect(err).To(MatchError(op.ErrNoSuchUser))
	})
	It("creates a user exclusively with its indexes as radosgw writes them", func(ctx SpecContext) {
		rec := &op.UserRecord{Info: alice(), Attrs: map[string][]byte{"user.rgw.iam-policy": []byte("p")}}
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		obj := c.Object(uidNS, "users.uid", "alice")
		Expect(obj).NotTo(BeNil())
		Expect(obj.Data).To(Equal(encode(meta.UserObject{UID: "alice", Info: alice()})), "RGWUID then RGWUserInfo, :296-298")
		Expect(obj.Xattrs).To(HaveKey("user.rgw.iam-policy"))
		Expect(c.Object(uidNS, "users.keys", "AKALICE").Data).To(Equal(encode(meta.UID("alice"))), "link_bl, :312-313")
		Expect(c.Object(uidNS, "users.email", "alice@example.com").Data).To(Equal(encode(meta.UID("alice"))))
		Expect(rec.Version.Ver).To(BeEquivalentTo(1))
		Expect(rec.Version.Tag).To(HaveLen(24))
		Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrUserAlreadyExists))
	})
	It("updates a user under its version, rewriting only the changed indexes", func(ctx SpecContext) {
		rec := &op.UserRecord{Info: alice()}
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		v := rec.Version
		rec.Info.Email = "alice2@example.com"
		rec.Info.AccessKeys = map[string]meta.AccessKey{"AKNEW": {ID: "AKNEW", Secret: "s", Active: true}}
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &v})).To(Succeed())
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: v.Tag}), "write_version = read_version + 1, :237-240")
		Expect(c.Object(uidNS, "users.keys", "AKNEW")).NotTo(BeNil())
		Expect(c.Object(uidNS, "users.keys", "AKALICE")).To(BeNil(), "remove_old_indexes, :424-432")
		Expect(c.Object(uidNS, "users.email", "alice@example.com")).To(BeNil())
		Expect(c.Object(uidNS, "users.email", "alice2@example.com")).NotTo(BeNil())
		stale := v
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{IfVersion: &stale})).To(MatchError(op.ErrConcurrentModification))
	})
	It("refuses an access key already mapped to another user", func(ctx SpecContext) {
		Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{Exclusive: true})).To(Succeed())
		bob := alice()
		bob.UserID, bob.Email = meta.UserID{ID: "bob"}, ""
		Expect(s.PutUser(ctx, &op.UserRecord{Info: bob}, op.PutUserOptions{Exclusive: true})).To(MatchError(op.ErrKeyExists), ":257-270")
		Expect(c.Object(uidNS, "users.uid", "bob")).To(BeNil(), "prepare fails before put")
	})
	It("removes a user with its indexes and bucket list", func(ctx SpecContext) {
		rec := &op.UserRecord{Info: alice()}
		Expect(s.PutUser(ctx, rec, op.PutUserOptions{Exclusive: true})).To(Succeed())
		c.Put(uidNS, "users.uid", "alice.buckets", nil)
		Expect(s.RemoveUser(ctx, rec)).To(Succeed())
		for _, o := range [][2]string{{"users.uid", "alice"}, {"users.uid", "alice.buckets"}, {"users.keys", "AKALICE"}, {"users.email", "alice@example.com"}} {
			Expect(c.Object(uidNS, o[0], o[1])).To(BeNil(), "%s/%s", o[0], o[1])
		}
		Expect(s.RemoveUser(ctx, rec)).To(MatchError(op.ErrNoSuchUser))
	})
	It("distributes a cache notify for every metadata write", func(ctx SpecContext) {
		Expect(s.PutUser(ctx, &op.UserRecord{Info: alice()}, op.PutUserOptions{Exclusive: true})).To(Succeed())
		var total int
		for i := range 8 {
			total += len(c.Notifies("ceph-objectstore.rgw.control", "", fmt.Sprintf("notify.%d", i)))
		}
		Expect(total).To(Equal(3), "the uid object, the key index and the email index")
	})
})
```

Add `Reads(pool, ns, oid string) int` to `fakerados.Cluster` (a per-object read-op counter) alongside `Notifies`.

- [ ] **Step 2: Run to see them fail; write `user.go`**

```go
package driver

func (s *Store) userObj(id meta.UserID) sysObj {
	return sysObj{pool: s.zone.Params.UserUIDPool, oid: id.String()}
}
func (s *Store) keyIndexObj(key string) sysObj { return sysObj{pool: s.zone.Params.UserKeysPool, oid: key} }
func (s *Store) emailIndexObj(email string) sysObj {
	return sysObj{pool: s.zone.Params.UserEmailPool, oid: strings.ToLower(email)}
}
func (s *Store) swiftIndexObj(name string) sysObj { return sysObj{pool: s.zone.Params.UserSwiftPool, oid: name} }

const bucketsSuffix = ".buckets" // RGW_BUCKETS_OBJ_SUFFIX

func (s *Store) ownerBucketsObj(owner meta.Owner) sysObj {
	if owner.User != nil {
		return sysObj{pool: s.zone.Params.UserUIDPool, oid: owner.User.String() + bucketsSuffix}
	}
	return sysObj{pool: s.zone.Params.AccountPool, oid: "buckets." + owner.Account}
}
func (s *Store) accountObj(id string) sysObj { return sysObj{pool: s.zone.Params.AccountPool, oid: "account." + id} }

func mapUserErr(err error) error {
	if errors.Is(err, radosclient.ErrCanceled) {
		return fmt.Errorf("%w: %w", op.ErrConcurrentModification, err)
	}
	return op.FromRADOS(err, op.ScopeUser)
}

func filterRGWAttrs(attrs map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, len(attrs))
	for k, v := range attrs {
		if strings.HasPrefix(k, meta.AttrPrefix) {
			out[k] = v
		}
	}
	return out
}

// GetUser is RGWSI_User_RADOS::read_user_info (svc_user_rados.cc:115-156).
func (s *Store) GetUser(ctx context.Context, id meta.UserID) (*op.UserRecord, error) {
	if id.ID == op.AnonymousUserID {
		return nil, op.ErrNoSuchUser
	}
	v := &objv{}
	res, err := s.sysobj.read(ctx, s.userObj(id), readParams{data: true, attrs: true, meta: true, objv: v})
	if err != nil {
		return nil, mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uo := meta.DecodeUserObject(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding user %s: %w", op.ErrInternalError, id, err)
	}
	if meta.ParseUserID(string(uo.UID)) != id {
		return nil, fmt.Errorf("%w: user object %s names %q", op.ErrInternalError, id, uo.UID)
	}
	return &op.UserRecord{Info: uo.Info, Attrs: filterRGWAttrs(res.attrs), Version: v.read, Mtime: res.mtime}, nil
}

// readIndex is read_index (:650-668): the RGWUID an index object points at.
func (s *Store) readIndex(ctx context.Context, o sysObj) (meta.UserID, error) {
	res, err := s.sysobj.read(ctx, o, readParams{data: true})
	if err != nil {
		return meta.UserID{}, mapUserErr(err)
	}
	d := denc.NewDecoder(res.data)
	uid := meta.DecodeUID(d)
	if err := d.Err(); err != nil {
		return meta.UserID{}, fmt.Errorf("%w: decoding index %s/%s: %w", op.ErrInternalError, o.pool, o.oid, err)
	}
	return meta.ParseUserID(string(uid)), nil
}

func (s *Store) GetUserByAccessKey(ctx context.Context, key string) (*op.UserRecord, error) {
	id, err := s.readIndex(ctx, s.keyIndexObj(key))
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*op.UserRecord, error) {
	id, err := s.readIndex(ctx, s.emailIndexObj(email))
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

func activeKeys(m map[string]meta.AccessKey) map[string]bool {
	out := map[string]bool{}
	for id, k := range m {
		if k.Active {
			out[id] = true
		}
	}
	return out
}

// PutUser is store_user_info's PutOperation (:190-511): prepare, put, complete.
func (s *Store) PutUser(ctx context.Context, rec *op.UserRecord, opts op.PutUserOptions) error {
	id := rec.Info.UserID
	var old *op.UserRecord
	if !opts.Exclusive {
		cur, err := s.GetUser(ctx, id)
		switch {
		case err == nil:
			old = cur
		case errors.Is(err, op.ErrNoSuchUser):
		default:
			return err
		}
	}
	v := &objv{}
	if opts.IfVersion != nil {
		v.read = *opts.IfVersion
		v.write = meta.ObjVersion{Ver: v.read.Ver + 1, Tag: v.read.Tag}
	} else {
		v.write = newWriteVersion()
	}
	var oldKeys, oldSwift map[string]bool
	oldEmail := ""
	if old != nil {
		oldKeys, oldSwift, oldEmail = activeKeys(old.Info.AccessKeys), activeKeys(old.Info.SwiftKeys), old.Info.Email
	}
	// prepare: an active key that another user already owns (:257-270)
	for key := range activeKeys(rec.Info.AccessKeys) {
		if oldKeys[key] {
			continue
		}
		owner, err := s.readIndex(ctx, s.keyIndexObj(key))
		if err == nil && owner != id {
			return fmt.Errorf("%w: access key %s belongs to %s", op.ErrKeyExists, key, owner)
		}
		if err != nil && !errors.Is(err, op.ErrNoSuchUser) {
			return err
		}
	}
	// put (:295-307)
	data := encodeAt(meta.UserObject{UID: meta.UID(id.String()), Info: rec.Info}, s.release)
	mtime, err := s.sysobj.write(ctx, s.userObj(id), data, rec.Attrs, opts.Exclusive, rec.Mtime, v)
	if err != nil {
		return mapUserErr(err)
	}
	rec.Version, rec.Mtime = v.read, mtime
	// complete (:309-351): the indexes, with the encoded RGWUID as data
	link := encodeAt(meta.UID(id.String()), s.release)
	if e := rec.Info.Email; e != "" && !strings.EqualFold(e, oldEmail) {
		if _, err := s.sysobj.write(ctx, s.emailIndexObj(e), link, nil, opts.Exclusive, time.Time{}, nil); err != nil {
			return mapUserErr(err)
		}
	}
	for key := range activeKeys(rec.Info.AccessKeys) {
		if oldKeys[key] {
			continue
		}
		if _, err := s.sysobj.write(ctx, s.keyIndexObj(key), link, nil, opts.Exclusive, time.Time{}, nil); err != nil {
			return mapUserErr(err)
		}
	}
	for name := range activeKeys(rec.Info.SwiftKeys) {
		if oldSwift[name] {
			continue
		}
		if _, err := s.sysobj.write(ctx, s.swiftIndexObj(name), link, nil, opts.Exclusive, time.Time{}, nil); err != nil {
			return mapUserErr(err)
		}
	}
	if old != nil {
		return s.removeOldIndexes(ctx, old.Info, rec.Info)
	}
	return nil
}

// removeOldIndexes is PutOperation::remove_old_indexes (:399-443).
func (s *Store) removeOldIndexes(ctx context.Context, old, cur meta.UserInfo) error {
	rm := func(o sysObj) error {
		if err := s.sysobj.remove(ctx, o, nil); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
			return mapUserErr(err)
		}
		return nil
	}
	if old.Email != "" && !strings.EqualFold(old.Email, cur.Email) {
		if err := rm(s.emailIndexObj(old.Email)); err != nil {
			return err
		}
	}
	newKeys, newSwift := activeKeys(cur.AccessKeys), activeKeys(cur.SwiftKeys)
	for key := range activeKeys(old.AccessKeys) {
		if !newKeys[key] {
			if err := rm(s.keyIndexObj(key)); err != nil {
				return err
			}
		}
	}
	for name := range activeKeys(old.SwiftKeys) {
		if !newSwift[name] {
			if err := rm(s.swiftIndexObj(name)); err != nil {
				return err
			}
		}
	}
	return nil
}

// RemoveUser is remove_user_info (:551-630).
func (s *Store) RemoveUser(ctx context.Context, rec *op.UserRecord) error {
	rm := func(o sysObj) error {
		if err := s.sysobj.remove(ctx, o, nil); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
			return mapUserErr(err)
		}
		return nil
	}
	for key := range activeKeys(rec.Info.AccessKeys) {
		if err := rm(s.keyIndexObj(key)); err != nil {
			return err
		}
	}
	for name := range activeKeys(rec.Info.SwiftKeys) {
		if err := rm(s.swiftIndexObj(name)); err != nil {
			return err
		}
	}
	if rec.Info.Email != "" {
		if err := rm(s.emailIndexObj(rec.Info.Email)); err != nil {
			return err
		}
	}
	if rec.Info.AccountID == "" {
		if err := rm(s.ownerBucketsObj(meta.UserOwner(rec.Info.UserID))); err != nil {
			return err
		}
	}
	v := &objv{read: rec.Version}
	if err := s.sysobj.remove(ctx, s.userObj(rec.Info.UserID), v); err != nil {
		return mapUserErr(err)
	}
	return nil
}

// readAccount reads account.<id> (account.cc:84-90).
func (s *Store) readAccount(ctx context.Context, id string) (meta.AccountInfo, error) {
	res, err := s.sysobj.read(ctx, s.accountObj(id), readParams{data: true})
	if err != nil {
		return meta.AccountInfo{}, op.FromRADOS(err, op.ScopeUser)
	}
	d := denc.NewDecoder(res.data)
	info := meta.DecodeAccountInfo(d)
	if err := d.Err(); err != nil {
		return meta.AccountInfo{}, fmt.Errorf("%w: decoding account %s: %w", op.ErrInternalError, id, err)
	}
	return info, nil
}
```

`op.FromRADOS(radosclient.ErrNotFound, op.ScopeUser)` is `op.ErrNoSuchUser` and `radosclient.ErrExists` at user scope is `op.ErrUserAlreadyExists` per G's table; `readIndex` therefore already yields `ErrNoSuchUser` for a missing index.

- [ ] **Step 3: Run the suite; expected PASS; run `make check`; commit**

`feat(driver): read and write users through the uid, key and email index objects`. Draft PR `feat(driver): user store (unit M task 4)`.

### Task 5: Bucket entry point and instance under cls_version

**Files:**
- Create: `internal/driver/bucket.go`, `internal/driver/bucket_test.go`
- Modify: `internal/driver/store.go` (drop the stubs these replace)

**Interfaces:**
- Consumes: Task 3's `sysobjs`, `objv`, `newWriteVersion`; `meta.BucketID` (`EntryPointOID`, `InstanceOID`), `meta.BucketEntryPoint`, `meta.DecodeBucketEntryPoint`, `meta.BucketInfo`, `meta.DecodeBucketInfo`; `op.BucketRecord`, `op.ErrNoSuchBucket`, `op.ErrBucketAlreadyExists`, `op.ErrConcurrentModification`, `op.ErrInternalError`, `op.FromRADOS`, `op.ScopeBucket`.
- Produces:

```go
package driver

// The two objects of a bucket, both in domain_root (svc_bucket_sobj.cc:20;
// meta.BucketID.EntryPointOID/InstanceOID).
func (s *Store) epObj(tenant, name string) sysObj
func (s *Store) instanceObj(b meta.BucketID) sysObj

// entryPoint is what read_bucket_entrypoint_info returns.
type entryPoint struct {
	ep    meta.BucketEntryPoint
	attrs map[string][]byte
	mtime time.Time
	v     objv
}
func (s *Store) readEntryPoint(ctx context.Context, tenant, name string) (*entryPoint, error)
func (s *Store) writeEntryPoint(ctx context.Context, ep meta.BucketEntryPoint, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error
func (s *Store) removeEntryPoint(ctx context.Context, tenant, name string, v *objv) error

// instance is what do_read_bucket_instance_info returns.
type instance struct {
	info  meta.BucketInfo
	attrs map[string][]byte
	mtime time.Time
	v     objv
}
func (s *Store) readInstance(ctx context.Context, b meta.BucketID) (*instance, error)
func (s *Store) writeInstance(ctx context.Context, info *meta.BucketInfo, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error
func (s *Store) removeInstance(ctx context.Context, b meta.BucketID, v *objv) error

// mapBucketErr: ECANCELED is a lost version race; the rest is op.FromRADOS at bucket scope.
func mapBucketErr(err error) error

// The op.BucketStore methods.
func (s *Store) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error)
func (s *Store) GetBucketInstance(ctx context.Context, id meta.BucketID) (*op.BucketRecord, error)
func (s *Store) PutBucketInfo(ctx context.Context, rec *op.BucketRecord) error
func (s *Store) PutBucketAttrs(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error
```

Semantics, from `svc_bucket_sobj.cc` and `rgw_bucket.cc` at v19.2.6:

- `readEntryPoint` ([:208-237](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/account.cc#L208-L237)): a versioned read of `<tenant>/<name>` (or `<name>`) with data, attrs, mtime; `ENOENT` → `ErrNoSuchBucket`; a record that does not decode is `ErrInternalError` (radosgw's `-EIO`).
- `readInstance` ([:343-372](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L343-L372)): the same on `.bucket.meta.<tenant>:<name>:<id>`; the info's version is the read version.
- `GetBucket` (`read_bucket_info`, [:374-486](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L374-L486)): the entry point, then, unless it carries an old-format `has_bucket_info` record ([:434-439](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L434-L439), served with the entry point's attrs and a zero instance version), the instance it names; the record's `Attrs` are the INSTANCE's ([:444-446](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L444-L446)), `Version` the instance's, `EPVersion` the entry point's, `Mtime` the instance's.
- `GetBucketInstance` (:385-392): the instance alone; `EntryPoint` holds only `Bucket` (radosgw skips the entry point when a bucket id is given).
- `PutBucketInfo` (`put_bucket_instance_info` → `store_bucket_instance_info`, [:489-565](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L489-L565)): write the instance non-exclusively under `rec.Version` (check EQ then inc), data = the info at `s.release`, attrs = `rec.Attrs`, mtime now; `ECANCELED` → `ErrConcurrentModification`; on success `rec.Version` is the incremented version and `rec.Mtime` the write time. `handle_overwrite` (the datasync flag's bilog start/stop, [:529-535](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L529-L535)) and `handle_bi_update` ([:542-547](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_bucket_sobj.cc#L542-L547)) are multisite's and are not run: a single zone has no bilog. A record whose entry point carried an old-format info (`Info.HasInstanceObj == false`) is refused with `ErrInternalError` naming the bucket: radosgw converts such a bucket on first write (`convert_old_bucket_info`, [rgw_bucket.cc:3286-3293](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3286-L3293)), a path no Rook cluster reaches and phase 1 does not port.
- `PutBucketAttrs` (`set_bucket_instance_attrs`, [rgw_bucket.cc:3277-3304](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3277-L3304), reached through `merge_and_store_attrs`, [rgw_sal_rados.cc:771-778](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L771-L778)): overlay `set`, delete `rm`, then `PutBucketInfo`; radosgw rewrites the whole instance object rather than setting xattrs alone, and the cache record it distributes carries the data.

- [ ] **Step 1: Write the failing specs**

`internal/driver/bucket_test.go`, package `driver_test`, with helpers `seedBucket(c, params, info, attrs, epVersion, instVersion)` writing both objects as radosgw does (`encode(meta.BucketEntryPoint{Bucket: info.Bucket, Owner: info.Owner, CreationTime: info.CreationTime, Linked: true})` at `domain_root/<tenant>/<name>`, `encode(info)` with the attrs at `domain_root/.bucket.meta.<tenant>:<name>:<id>`, each with its `ceph.objclass.version` xattr):

```go
var _ = Describe("BucketStore: entry point and instance", func() {
	var (
		c    *fakerados.Cluster
		s    *driver.Store
		root = "ceph-objectstore.rgw.meta"
	)
	BeforeEach(func(ctx SpecContext) {
		c = fakerados.New()
		c.SetRequiredOSDRelease("squid")
		c.RegisterClass("version", fakerados.VersionClass())
		seedRookZone(c, "ceph-objectstore", true)
		var err error
		s, err = driver.Open(ctx, c, conf(map[string]string{"rgw_zone": "ceph-objectstore"}), driver.Options{})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(s.Close)
	})
	plain := func(tenant string) meta.BucketInfo {
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Tenant: tenant, Name: "plain", Marker: "zone-ceph-objectstore.4155.1", ID: "zone-ceph-objectstore.4155.1"}
		info.Owner = meta.UserOwner(meta.UserID{Tenant: tenant, ID: "alice"})
		info.Zonegroup, info.PlacementRule = "zg-ceph-objectstore", meta.PlacementRule{Name: "default-placement"}
		info.CreationTime = meta.Time{Time: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}
		return info
	}
	It("reads a radosgw-written bucket: entry point, then the instance and its attrs", func(ctx SpecContext) {
		seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {9}}, meta.ObjVersion{Ver: 1, Tag: "ep"}, meta.ObjVersion{Ver: 4, Tag: "bi"})
		rec, err := s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.EntryPoint.Linked).To(BeTrue())
		Expect(rec.Info.Bucket.ID).To(Equal("zone-ceph-objectstore.4155.1"))
		Expect(rec.Attrs).To(HaveKeyWithValue("user.rgw.acl", []byte{9}), "attrs come from the instance, svc_bucket_sobj.cc:444-471")
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 4, Tag: "bi"}))
		Expect(rec.EPVersion).To(Equal(meta.ObjVersion{Ver: 1, Tag: "ep"}))
		Expect(c.Reads(root, "root", "plain")).To(Equal(1))
		_, err = s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(c.Reads(root, "root", "plain")).To(Equal(1), "served from the cache")
		Expect(c.Reads(root, "root", ".bucket.meta.plain:zone-ceph-objectstore.4155.1")).To(Equal(1))
	})
	It("names tenanted buckets the way radosgw does", func(ctx SpecContext) {
		seedBucket(c, plain("t1"), nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
		Expect(c.Object(root, "root", "t1/plain")).NotTo(BeNil())
		Expect(c.Object(root, "root", ".bucket.meta.t1:plain:zone-ceph-objectstore.4155.1")).NotTo(BeNil())
		rec, err := s.GetBucket(ctx, "t1", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.Bucket.Tenant).To(Equal("t1"))
	})
	It("is NoSuchBucket for a missing entry point, and caches the negative lookup", func(ctx SpecContext) {
		_, err := s.GetBucket(ctx, "", "nope")
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
		_, err = s.GetBucket(ctx, "", "nope")
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
		Expect(c.Reads(root, "root", "nope")).To(Equal(1))
	})
	It("reads an instance directly by id", func(ctx SpecContext) {
		info := plain("")
		seedBucket(c, info, nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 2, Tag: "b"})
		rec, err := s.GetBucketInstance(ctx, info.Bucket)
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
		Expect(rec.EntryPoint.Bucket).To(Equal(info.Bucket))
		Expect(c.Reads(root, "root", "plain")).To(BeZero(), "no entry point read, :385-392")
	})
	It("writes the instance under its version and reports a lost race", func(ctx SpecContext) {
		seedBucket(c, plain(""), nil, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
		rec, err := s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		stale := *rec
		rec.Info.Quota = meta.Quota{MaxObjects: 5, MaxSize: -1, Enabled: true}
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		Expect(rec.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}), "cls_version inc")
		Expect(s.PutBucketInfo(ctx, &stale)).To(MatchError(op.ErrConcurrentModification))
		got, err := s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(got.Info.Quota.MaxObjects).To(BeEquivalentTo(5), "the write updated the cache")
		Expect(got.Version).To(Equal(meta.ObjVersion{Ver: 2, Tag: "b"}))
	})
	It("merges attrs into a full instance write", func(ctx SpecContext) {
		seedBucket(c, plain(""), map[string][]byte{"user.rgw.acl": {1}, "user.rgw.x-amz-tagging": {2}}, meta.ObjVersion{Ver: 1, Tag: "e"}, meta.ObjVersion{Ver: 1, Tag: "b"})
		rec, err := s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(s.PutBucketAttrs(ctx, rec, map[string][]byte{"user.rgw.iam-policy": []byte("p")}, []string{"user.rgw.x-amz-tagging"})).To(Succeed())
		obj := c.Object(root, "root", ".bucket.meta.plain:zone-ceph-objectstore.4155.1")
		Expect(obj.Xattrs).To(HaveKey("user.rgw.iam-policy"))
		Expect(obj.Xattrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))
		Expect(obj.Xattrs).To(HaveKey("user.rgw.acl"))
		Expect(obj.Data).To(Equal(encode(rec.Info)), "the whole instance is rewritten, rgw_bucket.cc:3295-3302")
		Expect(rec.Attrs).NotTo(HaveKey("user.rgw.x-amz-tagging"))
	})
	It("serves an old-format entry point that carries its own bucket info, and refuses to rewrite it", func(ctx SpecContext) {
		info := plain("")
		info.HasInstanceObj = false
		ep := meta.NewBucketEntryPoint()
		ep.HasBucketInfo, ep.OldBucketInfo = true, &info
		c.Put(root, "root", "plain", encodeOldEntryPoint(info)) // a v7 RGWBucketEntryPoint: the bucket info itself; see meta.DecodeBucketEntryPoint
		rec, err := s.GetBucket(ctx, "", "plain")
		Expect(err).NotTo(HaveOccurred())
		Expect(rec.Info.Bucket.Name).To(Equal("plain"))
		Expect(s.PutBucketInfo(ctx, rec)).To(MatchError(op.ErrInternalError))
	})
})
```

`encodeOldEntryPoint` encodes `meta.BucketInfo` inside a v7 `RGWBucketEntryPoint` frame (`denc.Encoder.BeginStruct(7, 4)` around `info.Encode` is not the shape; the v7 entry point IS an `RGWBucketInfo` encoding whose struct version is below 8, so the helper encodes the info with `meta.BucketInfo.Encode` and re-labels the outer header version to 7 — write it with the phase-0 `meta` corpus fixture for an old entry point if `internal/meta/testdata` has one, else skip this spec with a `PIt` and a note in the PR that no old-format bucket exists on a Squid cluster).

- [ ] **Step 2: Run to see them fail; write `bucket.go`**

```go
package driver

func (s *Store) epObj(tenant, name string) sysObj {
	return sysObj{pool: s.zone.Params.DomainRoot, oid: meta.BucketID{Tenant: tenant, Name: name}.EntryPointOID()}
}
func (s *Store) instanceObj(b meta.BucketID) sysObj {
	return sysObj{pool: s.zone.Params.DomainRoot, oid: b.InstanceOID()}
}

func mapBucketErr(err error) error {
	if errors.Is(err, radosclient.ErrCanceled) {
		return fmt.Errorf("%w: %w", op.ErrConcurrentModification, err)
	}
	return op.FromRADOS(err, op.ScopeBucket)
}

func (s *Store) readEntryPoint(ctx context.Context, tenant, name string) (*entryPoint, error) {
	e := &entryPoint{}
	res, err := s.sysobj.read(ctx, s.epObj(tenant, name), readParams{data: true, attrs: true, meta: true, objv: &e.v})
	if err != nil {
		return nil, mapBucketErr(err)
	}
	d := denc.NewDecoder(res.data)
	e.ep = meta.DecodeBucketEntryPoint(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding bucket entry point %s/%s: %w", op.ErrInternalError, tenant, name, err)
	}
	e.attrs, e.mtime = res.attrs, res.mtime
	return e, nil
}

func (s *Store) writeEntryPoint(ctx context.Context, ep meta.BucketEntryPoint, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error {
	_, err := s.sysobj.write(ctx, s.epObj(ep.Bucket.Tenant, ep.Bucket.Name), encodeAt(ep, s.release), attrs, exclusive, mtime, v)
	return mapBucketErr(err)
}

func (s *Store) removeEntryPoint(ctx context.Context, tenant, name string, v *objv) error {
	return mapBucketErr(s.sysobj.remove(ctx, s.epObj(tenant, name), v))
}

func (s *Store) readInstance(ctx context.Context, b meta.BucketID) (*instance, error) {
	in := &instance{}
	res, err := s.sysobj.read(ctx, s.instanceObj(b), readParams{data: true, attrs: true, meta: true, objv: &in.v})
	if err != nil {
		return nil, mapBucketErr(err)
	}
	d := denc.NewDecoder(res.data)
	in.info = meta.DecodeBucketInfo(d)
	if err := d.Err(); err != nil {
		return nil, fmt.Errorf("%w: decoding bucket instance %s: %w", op.ErrInternalError, b.InstanceOID(), err)
	}
	in.attrs, in.mtime = res.attrs, res.mtime
	return in, nil
}

func (s *Store) writeInstance(ctx context.Context, info *meta.BucketInfo, attrs map[string][]byte, exclusive bool, mtime time.Time, v *objv) error {
	_, err := s.sysobj.write(ctx, s.instanceObj(info.Bucket), encodeAt(*info, s.release), attrs, exclusive, mtime, v)
	return mapBucketErr(err)
}

func (s *Store) removeInstance(ctx context.Context, b meta.BucketID, v *objv) error {
	return mapBucketErr(s.sysobj.remove(ctx, s.instanceObj(b), v))
}

// GetBucket is RGWSI_Bucket_SObj::read_bucket_info (svc_bucket_sobj.cc:374-486).
func (s *Store) GetBucket(ctx context.Context, tenant, name string) (*op.BucketRecord, error) {
	e, err := s.readEntryPoint(ctx, tenant, name)
	if err != nil {
		return nil, err
	}
	if e.ep.HasBucketInfo {
		info := *e.ep.OldBucketInfo
		info.Bucket.Tenant = tenant
		return &op.BucketRecord{EntryPoint: e.ep, Info: info, Attrs: e.attrs, EPVersion: e.v.read, Mtime: e.mtime}, nil
	}
	in, err := s.readInstance(ctx, e.ep.Bucket)
	if err != nil {
		return nil, err
	}
	return &op.BucketRecord{EntryPoint: e.ep, Info: in.info, Attrs: in.attrs, Version: in.v.read, EPVersion: e.v.read, Mtime: in.mtime}, nil
}

// GetBucketInstance is read_bucket_info with a bucket id (:385-392).
func (s *Store) GetBucketInstance(ctx context.Context, id meta.BucketID) (*op.BucketRecord, error) {
	in, err := s.readInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	ep := meta.NewBucketEntryPoint()
	ep.Bucket = in.info.Bucket
	return &op.BucketRecord{EntryPoint: ep, Info: in.info, Attrs: in.attrs, Version: in.v.read, Mtime: in.mtime}, nil
}

// PutBucketInfo is put_bucket_instance_info (rgw_rados.cc) →
// store_bucket_instance_info (svc_bucket_sobj.cc:489-565) for a single zone.
func (s *Store) PutBucketInfo(ctx context.Context, rec *op.BucketRecord) error {
	if !rec.Info.HasInstanceObj {
		return fmt.Errorf("%w: bucket %s has no instance object (pre-Infernalis format); convert it with radosgw-admin", op.ErrInternalError, rec.Info.Bucket.EntryPointOID())
	}
	v := &objv{read: rec.Version}
	now := s.now()
	if err := s.writeInstance(ctx, &rec.Info, rec.Attrs, false, now, v); err != nil {
		return err
	}
	rec.Version, rec.Mtime = v.read, now
	return nil
}

// PutBucketAttrs is merge_and_store_attrs → set_bucket_instance_attrs
// (rgw_sal_rados.cc:771-778, rgw_bucket.cc:3277-3304).
func (s *Store) PutBucketAttrs(ctx context.Context, rec *op.BucketRecord, set map[string][]byte, rm []string) error {
	attrs := maps.Clone(rec.Attrs)
	if attrs == nil {
		attrs = map[string][]byte{}
	}
	for _, k := range rm {
		delete(attrs, k)
	}
	maps.Copy(attrs, set)
	saved := rec.Attrs
	rec.Attrs = attrs
	if err := s.PutBucketInfo(ctx, rec); err != nil {
		rec.Attrs = saved
		return err
	}
	return nil
}
```

`s.now` is the injectable clock `Open` sets to `time.Now` (Task 3's sysobjs share it).

- [ ] **Step 3: Run the suite; expected PASS; `make check`; commit**

`feat(driver): read and write bucket entry points and instances under cls_version`. Draft PR `feat(driver): bucket entry point and instance (unit M task 5)`.

### Task 6: The cls_user bucket list, ListUserBuckets and owner stats sync

**Files:**
- Create: `internal/driver/ownerbuckets.go`, `internal/driver/ownerbuckets_test.go`, `internal/driver/index.go` (index pool and shard oids, shard headers), `internal/testutil/fakerados/cls_user.go`, `internal/testutil/fakerados/cls_rgw.go` (headers and `bucket_init_index` this task; listing, suggestions and usage in Tasks 8 and 10)
- Modify: `internal/driver/store.go`

**Interfaces:**
- Consumes: `cls/user` (`SetBucketsInfo`, `RemoveBucket`, `ListBuckets`, `GetHeader`, `CompleteStatsSync`, `BucketEntry`, `Bucket`, `Header`), `cls/rgw` (`GetDirHeader`, `BucketInitIndex`, `DirHeader`, `CategoryMain`), `meta.BucketEnt`, `meta.BucketInfo.IndexShardOID`, `op.Stats`, Task 1's `Placement`, Task 4's `ownerBucketsObj` and `readAccount`, `golang.org/x/sync/errgroup` (`SetLimit`).
- Produces:

```go
package driver

// indexPool is get_bucket_index_pool: the explicit placement's index pool
// when the bucket has one, else the zone placement's (svc_bi_rados.cc:43-70).
func (s *Store) indexPool(ctx context.Context, info *meta.BucketInfo) (radosclient.Pool, error)

// shardOIDs is get_bucket_index_objects for the current index layout
// (svc_bi_rados.cc:130-163): ".dir.<id>" alone for an unsharded index,
// ".dir.<id>.<shard>" for generation 0, ".dir.<id>.<gen>.<shard>" after.
func shardOIDs(info *meta.BucketInfo, gen meta.IndexLayoutGen) []string

// readShardHeaders is cls_bucket_head: one bucket_list of zero entries per
// shard, rgw_bucket_index_max_aio in flight, in shard order.
func (s *Store) readShardHeaders(ctx context.Context, info *meta.BucketInfo) ([]rgwcls.DirHeader, error)

// readIndexStats is RGWSI_BucketIndex_RADOS::read_stats (svc_bi_rados.cc:395-427):
// the Main category summed over the shards, as a BucketEnt.
func (s *Store) readIndexStats(ctx context.Context, info *meta.BucketInfo) (meta.BucketEnt, error)

// The cls_user side (driver/rados/buckets.cc).
func (s *Store) linkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID, created time.Time) error   // add: cls_user_set_buckets add=true (:45-60)
func (s *Store) unlinkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID) error                    // remove: cls_user_remove_bucket (:62-78)
func (s *Store) writeOwnerBucketStats(ctx context.Context, owner meta.Owner, ent meta.BucketEnt) error        // write_stats: add=false (:142-151)
func (s *Store) readOwnerStats(ctx context.Context, owner meta.Owner) (stats op.Stats, lastSync, lastUpdate time.Time, err error) // read_stats (:153-184), ENOENT → zero
func (s *Store) completeOwnerStatsSync(ctx context.Context, owner meta.Owner) error                            // cls_user_complete_stats_sync
func (s *Store) syncOwnerStats(ctx context.Context, owner meta.Owner, info *meta.BucketInfo) (meta.BucketEnt, error) // RGWBucketCtl::sync_owner_stats (rgw_bucket.cc:3474-3501)

// ownerTenant is the tenant listed buckets belong to: the user's, or the account's.
func (s *Store) ownerTenant(ctx context.Context, owner meta.Owner) (string, error)

// ListUserBuckets is rgwrados::buckets::list (buckets.cc:80-140).
func (s *Store) ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, max int) ([]meta.BucketEnt, string, bool, error)
```

```go
package fakerados

// UserClass emulates cls_user over the object's omap (one entry per bucket
// name, the header in the omap header): set_buckets_info, remove_bucket,
// list_buckets, get_header, complete_stats_sync, reset_user_stats2.
func UserClass() ClassFunc

// RGWClass emulates the rgw class over the object's omap (one entry per
// index key, the rgw_bucket_dir_header in the omap header):
// bucket_init_index (exclusive create of the header) and bucket_list.
func RGWClass() ClassFunc
```

In this task `RGWClass` implements `bucket_init_index` and `bucket_list` with zero entries (the header) only; Task 8 adds listing, `dir_suggest_changes` and `guard_bucket_resharding`, and Task 10 adds `user_usage_log_add`.

Semantics: `ListUserBuckets` loops `cls_user_bucket_list(marker, "", max - len)` on the owner's buckets object while the reply is truncated and fewer than `max` entries were collected ([:97-132](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L97-L132)); a missing buckets object is an empty list ([:108-111](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L108-L111)); each entry becomes a `meta.BucketEnt` with the tenant from the owner ([:119-131](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L119-L131)); `next` is the class's marker when truncated and `more` is that truncation ([:134-138](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L134-L138)). `readOwnerStats` is `cls_user_get_header`, `ENOENT` tolerated as zero ([:170-172](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L170-L172)). `syncOwnerStats` reads the Main-only index stats and writes them with `add=false` (the entry must exist, [:149](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/buckets.cc#L149)). `readShardHeaders` opens the index pool, names the shards from `info.Layout.Current` and issues one `GetDirHeader` per shard with at most `rgw_bucket_index_max_aio` in flight.

- [ ] **Step 1: Write the failing specs**

`internal/driver/ownerbuckets_test.go`, package `driver_test`, with `UserClass`, `RGWClass` and `VersionClass` registered and the Task 1 store; `seedShardHeader(c, indexPool, oid, hdr rgwcls.DirHeader)` writes an encoded header into the object's `OmapHdr`. The store is set up as in Task 5, plus `c.RegisterClass("user", fakerados.UserClass())` and `c.RegisterClass("rgw", fakerados.RGWClass())`:

```go
var _ = Describe("the owner's bucket list", func() {
	alice := meta.UserOwner(meta.UserID{ID: "alice"})
	bucket := func(name string) meta.BucketID {
		return meta.BucketID{Name: name, Marker: "m-" + name, ID: "m-" + name}
	}
	It("links buckets with cls_user and lists them in pages", func(ctx SpecContext) {
		for _, n := range []string{"c", "a", "b"} {
			Expect(s.linkBucket(ctx, alice, bucket(n), time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))).To(Succeed())
		}
		ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(HaveLen(2))
		Expect(ents[0].Bucket.Name).To(Equal("a"))
		Expect(ents[1].Bucket.Name).To(Equal("b"))
		Expect(more).To(BeTrue())
		Expect(next).To(Equal("b"), "the class's marker, buckets.cc:134-136")
		ents, next, more, err = s.ListUserBuckets(ctx, alice, next, 2)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(HaveLen(1))
		Expect(ents[0].Bucket.Name).To(Equal("c"))
		Expect(ents[0].Bucket.Marker).To(Equal("m-c"))
		Expect(more).To(BeFalse())
		Expect(next).To(BeEmpty())
	})
	It("lists nothing for an owner without a buckets object", func(ctx SpecContext) {
		ents, next, more, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred(), "ENOENT is an empty list, buckets.cc:108-111")
		Expect(ents).To(BeEmpty())
		Expect(next).To(BeEmpty())
		Expect(more).To(BeFalse())
	})
	It("gives listed buckets the owner's tenant", func(ctx SpecContext) {
		t1 := meta.UserOwner(meta.UserID{Tenant: "t1", ID: "alice"})
		Expect(s.linkBucket(ctx, t1, meta.BucketID{Tenant: "t1", Name: "x", Marker: "m", ID: "m"}, time.Now())).To(Succeed())
		Expect(c.Object("ceph-objectstore.rgw.meta", "users.uid", "t1$alice.buckets")).NotTo(BeNil())
		ents, _, _, err := s.ListUserBuckets(ctx, t1, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents[0].Bucket.Tenant).To(Equal("t1"))
	})
	It("uses the account's buckets object and tenant for an account owner", func(ctx SpecContext) {
		acct := meta.NewAccountInfo()
		acct.ID, acct.Tenant = "RGW00000000000000001", "t9"
		c.Put("ceph-objectstore.rgw.meta", "accounts", "account.RGW00000000000000001", encode(acct))
		owner := meta.AccountOwner("RGW00000000000000001")
		Expect(s.linkBucket(ctx, owner, bucket("z"), time.Now())).To(Succeed())
		Expect(c.Object("ceph-objectstore.rgw.meta", "accounts", "buckets.RGW00000000000000001")).NotTo(BeNil(), "account.cc:44-50")
		ents, _, _, err := s.ListUserBuckets(ctx, owner, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents[0].Bucket.Tenant).To(Equal("t9"))
	})
	It("unlinks a bucket", func(ctx SpecContext) {
		Expect(s.linkBucket(ctx, alice, bucket("a"), time.Now())).To(Succeed())
		Expect(s.unlinkBucket(ctx, alice, bucket("a"))).To(Succeed())
		ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(BeEmpty())
	})
	It("reads zero owner stats when the object is absent, and the header when it exists", func(ctx SpecContext) {
		stats, _, _, err := s.readOwnerStats(ctx, alice)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(Equal(op.Stats{}))
	})
	It("syncs a bucket's Main-category index stats into the owner's entry", func(ctx SpecContext) {
		info := meta.NewBucketInfo()
		info.Bucket = bucket("a")
		info.PlacementRule = meta.PlacementRule{Name: "default-placement"}
		info.Layout = meta.NewBucketLayout()
		info.Layout.Current.Layout.Normal.NumShards = 2
		index := "ceph-objectstore.rgw.buckets.index"
		seedShardHeader(c, index, ".dir.m-a.0", rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{rgwcls.CategoryMain: {TotalSize: 10, TotalSizeRounded: 4096, NumEntries: 1}, rgwcls.CategoryMultiMeta: {TotalSize: 99, NumEntries: 9}}})
		seedShardHeader(c, index, ".dir.m-a.1", rgwcls.DirHeader{Stats: map[uint8]rgwcls.CategoryStats{rgwcls.CategoryMain: {TotalSize: 5, TotalSizeRounded: 4096, NumEntries: 2}}})
		Expect(s.linkBucket(ctx, alice, info.Bucket, time.Now())).To(Succeed())
		ent, err := s.syncOwnerStats(ctx, alice, &info)
		Expect(err).NotTo(HaveOccurred())
		Expect(ent.Size).To(BeEquivalentTo(15), "Main only, svc_bi_rados.cc:414-421")
		Expect(ent.SizeRounded).To(BeEquivalentTo(8192))
		Expect(ent.Count).To(BeEquivalentTo(3))
		stats, _, _, err := s.readOwnerStats(ctx, alice)
		Expect(err).NotTo(HaveOccurred())
		Expect(stats).To(Equal(op.Stats{Size: 15, SizeRounded: 8192, NumObjects: 3}))
		ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents[0].Size).To(BeEquivalentTo(15))
	})
	It("names the shards of every layout generation", func() {
		info := meta.NewBucketInfo()
		info.Bucket = bucket("a")
		gen := meta.NewIndexLayoutGen()
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a"}), "unsharded")
		gen.Layout.Normal.NumShards = 2
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a.0", ".dir.m-a.1"}))
		gen.Gen = 3
		Expect(shardOIDs(&info, gen)).To(Equal([]string{".dir.m-a.3.0", ".dir.m-a.3.1"}), "svc_bi_rados.cc:119-128")
	})
})
```

(`shardOIDs` is unexported: put that one spec in a `package driver` file, `index_test.go`.)

- [ ] **Step 2: Write the two class emulators**

`fakerados/cls_user.go`: entries live in `obj.Omap[bucket name] = encode(cls_user_bucket_entry)`, the header in `obj.OmapHdr = encode(cls_user_header)`; `set_buckets_info` follows the phase-0 documentation of `SetBucketsInfo` (add creates or refreshes id and creation time, no-add updates stats of an existing entry; the header's stats are adjusted by the difference; an empty bucket name is `-EINVAL`); `remove_bucket` subtracts the entry's stats when it was synced; `list_buckets` returns entries after `marker` (exclusive) and before `end_marker`, at most `min(max, 1000)`, `truncated` and `marker` = the last returned name; `get_header` returns the header (zero when absent); `complete_stats_sync` sets `LastStatsSync`. Decode requests with `user.DecodeSetBucketsOp`, `DecodeRemoveBucketOp`, `DecodeListBucketsOp`, `DecodeCompleteStatsSyncOp`; encode replies with `user.ListBucketsRet`, `user.GetHeaderRet`.

`fakerados/cls_rgw.go`: `bucket_init_index` fails with `-EEXIST` when the header exists, else writes an empty encoded `rgwcls.DirHeader`; `bucket_list` decodes `rgwcls.DecodeListOp`, and with `NumEntries == 0` returns `rgwcls.ListRet{Dir: {Header: decoded OmapHdr}}`; every other method is `-EOPNOTSUPP` until Task 8.

- [ ] **Step 3: Write `index.go` and `ownerbuckets.go`**

```go
package driver

func (s *Store) indexPool(ctx context.Context, info *meta.BucketInfo) (radosclient.Pool, error) {
	if p := info.Bucket.ExplicitPlacement.IndexPool; p.Name != "" {
		return s.pools.get(ctx, p)
	}
	pl, err := s.Placement(info.PlacementRule)
	if err != nil {
		return nil, err
	}
	return s.pools.get(ctx, pl.IndexPool)
}

func shardOIDs(info *meta.BucketInfo, gen meta.IndexLayoutGen) []string {
	n := gen.Layout.Normal.NumShards
	if n == 0 {
		return []string{info.IndexShardOID(gen, 0)}
	}
	oids := make([]string, n)
	for i := range n {
		oids[i] = info.IndexShardOID(gen, i)
	}
	return oids
}
```

(`meta.BucketInfo.IndexShardOID(gen, shard)` already renders the three forms; the unsharded form ignores the shard.)

```go
func (s *Store) readShardHeaders(ctx context.Context, info *meta.BucketInfo) ([]rgwcls.DirHeader, error) {
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return nil, err
	}
	oids := shardOIDs(info, info.Layout.Current)
	headers := make([]rgwcls.DirHeader, len(oids))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.bucketIndexMaxAIO)
	for i, oid := range oids {
		g.Go(func() error {
			rop := radosclient.NewReadOp()
			res := rgwcls.GetDirHeader(rop, s.release)
			if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
				return fmt.Errorf("reading index shard %s: %w", oid, err)
			}
			ret, err := res.Result()
			if err != nil {
				return err
			}
			headers[i] = ret.Dir.Header
			return nil
		})
	}
	return headers, g.Wait()
}

func (s *Store) readIndexStats(ctx context.Context, info *meta.BucketInfo) (meta.BucketEnt, error) {
	headers, err := s.readShardHeaders(ctx, info)
	if err != nil {
		return meta.BucketEnt{}, err
	}
	ent := meta.BucketEnt{Bucket: info.Bucket, PlacementRule: info.PlacementRule, CreationTime: info.CreationTime}
	for _, h := range headers {
		if st, ok := h.Stats[rgwcls.CategoryMain]; ok {
			ent.Count += st.NumEntries
			ent.Size += st.TotalSize
			ent.SizeRounded += st.TotalSizeRounded
		}
	}
	return ent, nil
}
```

`s.opts.bucketIndexMaxAIO` is `rgw_bucket_index_max_aio` (128), read in Task 12's `options.go`; until then read it inline in `Open`.

```go
func clsBucket(b meta.BucketID) user.Bucket {
	return user.Bucket{Name: b.Name, Marker: b.Marker, BucketID: b.ID}
}

func (s *Store) ownerBucketsPool(ctx context.Context, owner meta.Owner) (radosclient.Pool, string, error) {
	o := s.ownerBucketsObj(owner)
	p, err := s.pools.get(ctx, o.pool)
	return p, o.oid, err
}

func (s *Store) linkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID, created time.Time) error {
	if created.IsZero() {
		created = s.now()
	}
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.SetBucketsInfo(wop, []user.BucketEntry{{Bucket: clsBucket(b), CreationTime: created}}, true, s.now(), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

func (s *Store) unlinkBucket(ctx context.Context, owner meta.Owner, b meta.BucketID) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.RemoveBucket(wop, clsBucket(b), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

func (s *Store) writeOwnerBucketStats(ctx context.Context, owner meta.Owner, ent meta.BucketEnt) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	entry := user.BucketEntry{Bucket: clsBucket(ent.Bucket), Size: ent.Size, SizeRounded: ent.SizeRounded, Count: ent.Count, UserStatsSync: true, CreationTime: ent.CreationTime.Time}
	wop := radosclient.NewWriteOp()
	user.SetBucketsInfo(wop, []user.BucketEntry{entry}, false, s.now(), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

func (s *Store) readOwnerStats(ctx context.Context, owner meta.Owner) (op.Stats, time.Time, time.Time, error) {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return op.Stats{}, time.Time{}, time.Time{}, err
	}
	rop := radosclient.NewReadOp()
	res := user.GetHeader(rop, s.release)
	if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
		if errors.Is(err, radosclient.ErrNotFound) {
			return op.Stats{}, time.Time{}, time.Time{}, nil
		}
		return op.Stats{}, time.Time{}, time.Time{}, err
	}
	h, err := res.Header()
	if err != nil {
		return op.Stats{}, time.Time{}, time.Time{}, err
	}
	return op.Stats{Size: h.Stats.TotalBytes, SizeRounded: h.Stats.TotalBytesRounded, NumObjects: h.Stats.TotalEntries}, h.LastStatsSync, h.LastStatsUpdate, nil
}

func (s *Store) completeOwnerStatsSync(ctx context.Context, owner meta.Owner) error {
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return err
	}
	wop := radosclient.NewWriteOp()
	user.CompleteStatsSync(wop, s.now(), s.release)
	_, err = pool.Write(ctx, oid, wop, radosclient.OpFlagNone)
	return err
}

func (s *Store) syncOwnerStats(ctx context.Context, owner meta.Owner, info *meta.BucketInfo) (meta.BucketEnt, error) {
	ent, err := s.readIndexStats(ctx, info)
	if err != nil {
		return meta.BucketEnt{}, err
	}
	return ent, s.writeOwnerBucketStats(ctx, owner, ent)
}

func (s *Store) ownerTenant(ctx context.Context, owner meta.Owner) (string, error) {
	if owner.User != nil {
		return owner.User.Tenant, nil
	}
	acct, err := s.readAccount(ctx, owner.Account)
	if err != nil {
		return "", err
	}
	return acct.Tenant, nil
}

// ListUserBuckets is rgwrados::buckets::list (buckets.cc:80-140).
func (s *Store) ListUserBuckets(ctx context.Context, owner meta.Owner, marker string, max int) ([]meta.BucketEnt, string, bool, error) {
	tenant, err := s.ownerTenant(ctx, owner)
	if err != nil {
		return nil, "", false, err
	}
	pool, oid, err := s.ownerBucketsPool(ctx, owner)
	if err != nil {
		return nil, "", false, err
	}
	var ents []meta.BucketEnt
	truncated := false
	for {
		count := max - len(ents)
		rop := radosclient.NewReadOp()
		res := user.ListBuckets(rop, marker, "", int32(min(count, math.MaxInt32)), s.release) //nolint:gosec // bounded above
		if _, err := pool.Read(ctx, oid, rop, radosclient.OpFlagNone); err != nil {
			if errors.Is(err, radosclient.ErrNotFound) {
				return ents, "", false, nil
			}
			return nil, "", false, err
		}
		entries, next, more, err := res.Entries()
		if err != nil {
			return nil, "", false, err
		}
		for _, e := range entries {
			ents = append(ents, meta.BucketEnt{
				Bucket:       meta.BucketID{Tenant: tenant, Name: e.Bucket.Name, Marker: e.Bucket.Marker, ID: e.Bucket.BucketID},
				Size:         e.Size,
				SizeRounded:  e.SizeRounded,
				CreationTime: meta.Time{Time: e.CreationTime},
				Count:        e.Count,
			})
		}
		marker, truncated = next, more
		if !truncated || len(ents) >= max {
			break
		}
	}
	if !truncated {
		marker = ""
	}
	return ents, marker, truncated, nil
}
```

- [ ] **Step 4: Run the suite; expected PASS; `make check`; commit**

`feat(driver): link, unlink and list an owner's buckets through cls_user and read index stats`. Draft PR `feat(driver): owner bucket list and index stats (unit M task 6)`.

### Task 7: Bucket Create, Delete, Head and GetLocation: driver, ops, handlers

**Files:**
- Create: `internal/driver/bucketops.go`, `internal/driver/bucketops_test.go`, `internal/op/bucketops.go`, `internal/op/bucketops_test.go`, `internal/op/usagelog.go`, `internal/op/usagelog_test.go`, `internal/s3/bucketname.go`, `internal/s3/bucketname_test.go`, `internal/s3/bucket_test.go`
- Modify: `internal/s3/bucket.go` (`bucketHandlers()` gains `create_bucket`, `delete_bucket`, `stat_bucket`, `get_bucket_location`), `internal/driver/store.go`, `internal/testutil/fakerados/cluster.go` (`AfterWrite` hook)

**Interfaces:**
- Consumes: Tasks 5, 6 and 8 (`writeInstance`, `writeEntryPoint`, `readEntryPoint`, `removeEntryPoint`, `removeInstance`, `linkBucket`, `unlinkBucket`, `syncOwnerStats`, `listUnordered`, `indexPool`, `shardOIDs`), `cls/rgw.BucketInitIndex`, `radosclient.Cluster.InstanceID`, `meta.NewBucketLayout`, `meta.LogLayoutFromIndex`, `meta.IndexNormal`, `meta.IndexIndexless`; Z's `authz.Evaluator.BuildDefaultACL` and `authz.UserResolver`, `acl.Policy.Encode`; `op.CreateBucketParams`, `op.ErrBucketAlreadyExists`, `op.ErrBucketNotEmpty`, `op.ErrTooManyBuckets`, `op.ErrInvalidLocationConstraint`, `op.ErrIllegalLocationConstraint`, `op.ErrZonegroupPlacementMisconfig`, `op.ErrInvalidBucketName`, `op.ErrInvalidArgument`, `op.ErrAccessDenied`, `op.ErrNoSuchBucket`, `op.ErrMalformedXML`; `s3.WriteXML`, `s3.Config.RelaxedBucketNames`, `cephconf.Options` (`rgw_list_buckets_max_chunk`, `rgw_max_put_param_size`, `rgw_override_bucket_index_max_shards`, `rgw_relaxed_s3_bucket_names`). G's `xmltext.Escape` (Task 4) for the location document's text.
- Produces:

```go
package driver

// bucketID is create_bucket_id (rgw_rados.cc:2344-2352): "<zone id>.<instance id>.<n>".
func (s *Store) bucketID() string

// defaultLayout is init_default_bucket_layout (driver/rados/rgw_bucket.cc:2781-2801).
func (s *Store) defaultLayout(indexType meta.IndexType) meta.BucketLayout

// initIndex is CLSRGWIssueBucketIndexInit for the current layout: an exclusive
// create plus bucket_init_index on every shard, rgw_bucket_index_max_aio in flight
// (svc_bi_rados.cc:354-372). cleanIndex removes them (:374-393).
func (s *Store) initIndex(ctx context.Context, info *meta.BucketInfo) error
func (s *Store) cleanIndex(ctx context.Context, info *meta.BucketInfo) error

// checkBucketEmpty is RGWRados::check_bucket_empty (rgw_rados.cc:5186-5231).
func (s *Store) checkBucketEmpty(ctx context.Context, rec *op.BucketRecord) error

// The op.BucketStore methods.
func (s *Store) CreateBucket(ctx context.Context, p op.CreateBucketParams) (*op.BucketRecord, error)
func (s *Store) DeleteBucket(ctx context.Context, rec *op.BucketRecord) error
```

```go
package op

// LogUsage is rgw_log.cc's log_usage (:195-251): the usage entry of one
// finished request, handed to Env.Usage. Ops call it from Complete.
func LogUsage(ctx context.Context, r *Request, opName string)

// CreateBucket is RGWCreateBucket (rgw_op.cc:3215-3244, :3458-3707) with
// RGWCreateBucket_ObjStore_S3::get_params' fields (rgw_rest_s3.cc:2473-2542).
type CreateBucket struct {
	LocationConstraint string            // the XML body's, "" when absent; "zg:placement" already split by the handler
	Placement          meta.PlacementRule // the placement half of the constraint, and x-amz-storage-class
	ObjLockEnabled     bool
	ACL                acl.Policy        // built by the handler from the canned ACL or grant headers
	Existed            bool              // set when the bucket already existed and the caller owns it (200 for S3)
	Result             *BucketRecord
}

// DeleteBucket is RGWDeleteBucket (rgw_op.cc:3709-3804).
type DeleteBucket struct{}

// StatBucket is RGWStatBucket (rgw_op.cc:2983-3031; v20.2.4 :3250-3265).
type StatBucket struct {
	ReadStats bool  // Tentacle: only with ?read-stats; Squid: always
	Stats     Stats // the Main category
}

// GetBucketLocation is RGWGetBucketLocation (rgw_op.cc:3136-3147; rgw_rest_s3.cc:2129-2150).
type GetBucketLocation struct {
	APIName string
}
```

```go
package s3

// ValidBucketName is valid_s3_bucket_name (rgw_rest_s3.h:838-905).
func ValidBucketName(name string, relaxed bool) error // nil or op.ErrInvalidBucketName
```

Driver semantics, from the C++ cited:

- `CreateBucket` (`RGWRados::create_bucket` [:2354-2458](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L2354-L2458), `RadosBucket::create` [rgw_sal_rados.cc:159-231](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L159-L231), `put_linked_bucket_info` [:9039-9075](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L9039-L9075), `do_link_bucket` [rgw_bucket.cc:3336-3405](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L3336-L3405)): resolve the placement (`Placement(p.Placement)`); up to 20 attempts: a fresh `meta.NewBucketInfo()` with `Bucket{Tenant, Name, Marker: id, ID: id}`, `Owner`, `Zonegroup`, `PlacementRule`, `HasInstanceObj = true`, `CreationTime = now`, `Quota`, `RequesterPays = false`, `Layout = defaultLayout(zone placement's index type)`, obj-lock flags when asked (`BucketVersioned|BucketObjLockEnabled`); `initIndex` unless indexless; the instance written exclusively with a fresh version (`ECANCELED` → treated as `EEXIST`); the entry point `{Bucket, Owner, CreationTime, Linked: true}` written exclusively with its own fresh version; on `EEXIST` from either: read the existing bucket by name — `ErrNoSuchBucket` → next attempt; a different instance id → `cleanIndex` and `removeInstance` of the new one (failures logged); return the EXISTING record with `ErrBucketAlreadyExists` so the op compares owners ([:173-185](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_bucket.cc#L173-L185) and [rgw_rest_s3.cc:2546-2547](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2546-L2547) decide the status); then `linkBucket(owner, bucket, creationTime)` (the cls_user add, `update_entrypoint=false` since the entry point was just written linked); a link failure unlinks and returns the error; finally re-read the entry point and, when it is gone (a concurrent DELETE won, [:210-227](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L210-L227)), unlink and still return the record. Twenty losses → `ErrNoSuchBucket` with radosgw's "continuously raced" message ([:2455-2457](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2455-L2457)).
- `checkBucketEmpty`: nothing when `rec.Info.Zonegroup` is not this zonegroup's id (:5188-5192); `listUnordered` pages of 1000 with `listVersions=true` from an empty marker; any entry whose index name parses into the EMPTY namespace → `ErrBucketNotEmpty` (:5220-5227).
- `DeleteBucket` (`RadosBucket::remove` [:350-468](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L350-L468) and `RGWRados::delete_bucket` [:5238-5302](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5238-L5302), for `delete_children=false`): `own := rec.Info.Zonegroup == zonegroup id`; when own: `syncOwnerStats` (failure logged at warning), then `checkBucketEmpty`, then the multipart abort ([:395-400](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L395-L400); v20.2.4 :413-418), which is P Task 7 Steps 6-8's `abortMultiparts`, inserted at the comment the code below leaves inside the `own` branch — until P lands, a bucket whose only contents are in-flight uploads is deleted without aborting them; no rgw-go-created upload can exist before P, so the gap is a radosgw-created upload on a shared zone, and Task 7's PR description says so; the entry-point removal under `rec.EPVersion` — when it is zero the entry point is re-read with a tracker and skipped when it names a different bucket id or the read fails (:5256-5277); `ECANCELED` on the removal is success, since the other deleter already unlinked ([rgw_op.cc:3793-3797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3793-L3797)); then `removeInstance` (ENOENT tolerated) and `cleanIndex` best-effort ([:5287-5299](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5287-L5299)); finally `unlinkBucket` ([:461-465](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L461-L465)), whose error is returned.

Op semantics (`rgw_op.cc`): `CreateBucket.VerifyPermission` — anonymous → `ErrAccessDenied`; `VerifyUserPermission(ctx, r, policy.S3CreateBucket)`; identity tenant ≠ `r.Tenant` for a non-role identity → `ErrAccessDenied`; `checkOwnerMaxBuckets` ([:3170-3213](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3170-L3213)): `remaining` = `Identity.User.MaxBuckets` or the account's `MaxBuckets` (read through `Env.Users`? accounts are not in `UserStore`; use `Identity.Account.MaxBuckets`, which A fills), `< 0` → `ErrAccessDenied` (EPERM), `0` → unlimited, else page `ListUserBuckets` in chunks of `max(rgw_list_buckets_max_chunk, remaining)` and `ErrTooManyBuckets` when `remaining <= 0`. `Execute` ([:3458-3707](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3458-L3707)): the location constraint (with a period: `zonegroups_by_api` built from `Env.Zone.Period().PeriodMap.ZoneGroups` by `APIName`, else `ErrInvalidLocationConstraint.WithMessage("The <lc> location constraint is not valid.")`; without: must equal `ZoneGroup().APIName` else `ErrIllegalLocationConstraint.WithMessage("The <lc> location constraint is incompatible for the region specific endpoint this request was sent to.")`; a user request must land on this zonegroup, same error); `Zonegroup = bucket zonegroup id`; `selectBucketPlacement` ([:3416-3456](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3416-L3456)): requested > `Identity.User.DefaultPlacement` > `ZoneGroup().DefaultPlacement`, empty → `ErrZonegroupPlacementMisconfig`; not a `PlacementTargets` key → `ErrInvalidLocationConstraint`; `user_permitted(Identity.User.PlacementTags)` else `ErrAccessDenied`; `Env.Zone.Placement(rule)` else `ErrInvalidLocationConstraint`; the existing bucket (`Env.Buckets.GetBucket`, `ErrNoSuchBucket` ignored): zonegroup differs → `ErrBucketAlreadyExists.WithMessage("Cannot modify existing bucket's zonegroup")`, placement differs → `"Cannot modify existing bucket's placement rule"`, ACL differs (decode `user.rgw.acl`, compare with `o.ACL`) → `"Cannot modify existing access control policy"`; `Owner = o.ACL.Owner` parsed with `meta.ParseOwner`; `Attrs["user.rgw.acl"] = encode(o.ACL)`; `Env.Buckets.CreateBucket`; `ErrBucketAlreadyExists` with a record whose `Info.Owner` equals the caller's owner → `Existed = true`, success (radosgw's `-ERR_BUCKET_EXISTS` → 200); a different owner → the error (409). `Complete` → `LogUsage`.

`DeleteBucket`: `Init` loads the bucket (404 when missing); `VerifyBucketPermission(S3DeleteBucket)`; `Execute` → `Env.Buckets.DeleteBucket(r.BucketRec)`; `ErrConcurrentModification` → success ([rgw_op.cc:3793-3797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3793-L3797)). `StatBucket`: `VerifyBucketPermission(S3ListBucket)` ([:2989-2991](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L2989-L2991)); `Execute`: `ReadStats` → `Env.Stats.BucketStats(rec)` (Main only, M-D7). `GetBucketLocation`: `VerifyBucketPermission(S3GetBucketLocation)`; `APIName` = the bucket zonegroup's `APIName` when it is this zonegroup or a period-map zonegroup, else the zonegroup id unless it is `"default"`, else `""` ([rgw_rest_s3.cc:2138-2145](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2138-L2145)).

`LogUsage` ([rgw_log.cc:195-251](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L195-L251)): nothing for `Identity.System`; `owner = Identity.Owner`; when `r.Bucket != ""`: `owner = r.BucketRec.Info.Owner` when the record exists, `payer = Identity.Owner` when `RequesterPays` and `r.Status != 403`; `bucket = r.Bucket`, `"-"` when `r.Status == 404`; `Ops = 1`, `SuccessfulOps = 1` when `200 <= Status <= 399`; `BytesSent = r.BytesOut`, `BytesReceived = r.BytesIn`; `Time = r.Env.Clock()`; the category is the op name; `Env.Usage.Log`. G's `UsageEntry` has `Owner` and no payer: when a payer exists it goes in `Owner`, which is what `insert_user` keys by ([:159-165](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L159-L165)); the stored record's separate `payer` field is Task 10's to fill from a second `UsageEntry` field — add `Payer meta.Owner` to `op.UsageEntry` (additive) so Task 10 stores both.

Handler semantics (`rgw_rest_s3.cc`): `create_bucket`: `ValidBucketName(r.Bucket, cfg.RelaxedBucketNames)`; the body (at most `rgw_max_put_param_size`; more → `ErrInvalidRange`, 416, because `read_all_input` answers an oversized body with `-ERANGE` ([rgw_rest.cc:1551-1552](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L1551-L1552)) and `get_params` returns it unchanged ([rgw_rest_s3.cc:2491-2494](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2491-L2494) at v19.2.6, [:2611-2614](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2611-L2614) at v20.2.4; `ERANGE` is InvalidRange, [rgw_common.cc:128](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L128))) parsed as `CreateBucketConfiguration/LocationConstraint` (a body that is not that document → `ErrInvalidArgument`, [:2512-2520](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2512-L2520)); `"zg:placement"` → `Placement.Name` and the constraint; `x-amz-storage-class` → `Placement.StorageClass`; `x-amz-bucket-object-lock-enabled` must be `true`/`false` (case-insensitive) else `ErrInvalidArgument`; the ACL from `authz.BuildDefaultACL(ctx, r, resolver, owner, owner)` where `owner` is the identity's `acl.Owner{ID, DisplayName}` (a canned ACL together with grant headers is `ErrInvalidRequest`, [:2412-2414](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2412-L2414), inside Z's builder); response 200 with no body. `delete_bucket` → 204. `stat_bucket` (HEAD) → 200, no body, headers per release: Squid `X-RGW-Object-Count`, `X-RGW-Bytes-Used` always, and for the owner `X-RGW-Quota-User-Size`, `X-RGW-Quota-User-Objects`, `X-RGW-Quota-Max-Buckets`, `X-RGW-Quota-Bucket-Size`, `X-RGW-Quota-Bucket-Objects` ([:2377-2393](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_common.cc#L2377-L2393)); Tentacle: the two counts only with `?read-stats`, `X-RGW-Quota-Max-Buckets` for the owner, the user quota pair only when `UserQuota.Enabled`, the bucket pair only when the bucket quota is enabled (v20.2.4 [:2480-2515](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2480-L2515)). `get_bucket_location` → `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">api</LocationConstraint>` with the api name through `xmltext.Escape` (`dump_format_ns` escapes it, [rgw_rest_s3.cc:2147-2148](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2147-L2148) at v19.2.6, [:2250](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2250) at v20.2.4), a `Content-Length` and no `Accept-Ranges` (`end_header` names no length); no `Content-Type` on Squid, where `end_header(s, this)` ([:2132](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2132)) runs before `dump_start` ([:2133](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2133)) so the formatter is still empty and no type is set ([rgw_rest.cc:613-619](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L613-L619)), and `Content-Type: application/xml` on Tentacle, which passes `to_mime_type(s->format)` (`[T]` [:2235](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2235)) (on Squid the handler sets the header to nil, or net/http would sniff one); and on Tentacle the header `x-rgw-bucket-placement-target: <rule name>`.

- [ ] **Step 1: Write the failing driver specs**

`internal/driver/bucketops_test.go`, package `driver_test` (store as in Task 6 with the three emulators):

```go
var _ = Describe("CreateBucket and DeleteBucket", func() {
	alice := meta.UserOwner(meta.UserID{ID: "alice"})
	params := func(name string) op.CreateBucketParams {
		return op.CreateBucketParams{Name: name, Owner: alice, Zonegroup: "zg-ceph-objectstore",
			Placement: meta.PlacementRule{Name: "default-placement"}, Attrs: map[string][]byte{"user.rgw.acl": {1}}, Exclusive: true}
	}
	It("creates the index shards, the instance, the entry point and the owner link as radosgw does", func(ctx SpecContext) {
		rec, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred())
		id := rec.Info.Bucket.ID
		Expect(id).To(HavePrefix("zone-ceph-objectstore."), "create_bucket_id: <zone id>.<instance>.<n>")
		Expect(rec.Info.Bucket.Marker).To(Equal(id))
		Expect(rec.Info.Layout.Current.Layout.Normal.NumShards).To(BeEquivalentTo(11), "zone.bucket_index_max_shards")
		Expect(rec.Info.Layout.Logs).To(HaveLen(1), "init_default_bucket_layout adds the in-index log layout")
		for i := range 11 {
			Expect(c.Object("ceph-objectstore.rgw.buckets.index", "", fmt.Sprintf(".dir.%s.%d", id, i))).NotTo(BeNil(), "shard %d", i)
		}
		Expect(c.Object("ceph-objectstore.rgw.meta", "root", ".bucket.meta.plain:"+id)).NotTo(BeNil())
		ep := c.Object("ceph-objectstore.rgw.meta", "root", "plain")
		Expect(ep).NotTo(BeNil())
		Expect(rec.EntryPoint.Linked).To(BeTrue())
		Expect(rec.Version.Ver).To(BeEquivalentTo(1))
		Expect(rec.EPVersion.Ver).To(BeEquivalentTo(1))
		ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(HaveLen(1))
		Expect(ents[0].Bucket.ID).To(Equal(id))
	})
	It("returns the existing record with BucketAlreadyExists and cleans up the losing instance", func(ctx SpecContext) {
		first, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred())
		again, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).To(MatchError(op.ErrBucketAlreadyExists))
		Expect(again.Info.Bucket.ID).To(Equal(first.Info.Bucket.ID), "the caller decides by owner (rgw_sal_rados.cc:173-185)")
		var instances int
		for oid := range c.Objects("ceph-objectstore.rgw.meta", "root") {
			if strings.HasPrefix(oid, ".bucket.meta.plain:") {
				instances++
			}
		}
		Expect(instances).To(Equal(1), "the loser's instance was removed, rgw_rados.cc:2434-2447")
		Expect(c.Objects("ceph-objectstore.rgw.buckets.index", "")).To(HaveLen(11), "and its shards")
	})
	It("unlinks when the entry point was deleted concurrently", func(ctx SpecContext) {
		c.AfterWrite("ceph-objectstore.rgw.meta", "root", "plain", func() { c.Delete("ceph-objectstore.rgw.meta", "root", "plain") })
		rec, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred(), "rgw_sal_rados.cc:210-227 answers success")
		Expect(rec).NotTo(BeNil())
		ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(BeEmpty(), "unlinked")
	})
	It("refuses to delete a bucket with objects and deletes an empty one completely", func(ctx SpecContext) {
		rec, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred())
		shard := ".dir." + rec.Info.Bucket.ID + "." + fmt.Sprint(shardOf(rec, "k"))
		seedIndexEntry(c, "ceph-objectstore.rgw.buckets.index", shard, entry("k", 1))
		Expect(s.DeleteBucket(ctx, rec)).To(MatchError(op.ErrBucketNotEmpty))
		c.Object("ceph-objectstore.rgw.buckets.index", "", shard).Omap = map[string][]byte{}
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed())
		Expect(c.Object("ceph-objectstore.rgw.meta", "root", "plain")).To(BeNil())
		Expect(c.Object("ceph-objectstore.rgw.meta", "root", ".bucket.meta.plain:"+rec.Info.Bucket.ID)).To(BeNil())
		Expect(c.Objects("ceph-objectstore.rgw.buckets.index", "")).To(BeEmpty(), "clean_index")
		ents, _, _, err := s.ListUserBuckets(ctx, alice, "", 10)
		Expect(err).NotTo(HaveOccurred())
		Expect(ents).To(BeEmpty())
		_, err = s.GetBucket(ctx, "", "plain")
		Expect(err).To(MatchError(op.ErrNoSuchBucket))
	})
	It("does not count a multipart-namespace entry as content", func(ctx SpecContext) {
		rec, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred())
		shard := ".dir." + rec.Info.Bucket.ID + ".0"
		seedIndexEntry(c, "ceph-objectstore.rgw.buckets.index", shard, entry("_multipart_k.2~abc.meta", 0))
		Expect(s.DeleteBucket(ctx, rec)).To(Succeed(), "rgw_rados.cc:5220-5227 checks the empty namespace only")
	})
	It("syncs the owner's stats before deleting and tolerates a lost entry-point race", func(ctx SpecContext) {
		rec, err := s.CreateBucket(ctx, params("plain"))
		Expect(err).NotTo(HaveOccurred())
		stale := *rec
		Expect(s.PutBucketInfo(ctx, rec)).To(Succeed())
		c.Object("ceph-objectstore.rgw.meta", "root", "plain").Xattrs[version.XattrName] = encode(version.ObjVersion{Ver: 9, Tag: "other"})
		Expect(s.DeleteBucket(ctx, &stale)).To(Succeed(), "ECANCELED on the entry point is success, rgw_op.cc:3793-3797")
	})
})
```

`fakerados` gains `Objects(pool, ns) map[string]*Object`, `Delete(pool, ns, oid)` and `AfterWrite(pool, ns, oid, fn)`. The "does not count a multipart-namespace entry as content" spec stays green once P Task 7 Step 7 inserts `abortMultiparts`: the seeded entry has no meta object, so the abort answers `ErrNoSuchUpload` and is skipped, as `abort_multiparts` skips `-ERR_NO_SUCH_UPLOAD` ([rgw_sal_rados.cc:990](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_sal_rados.cc#L990) at v19.2.6).

- [ ] **Step 2: Write the failing op and handler specs**

`internal/op/bucketops_test.go` on `memstore` seeded with `alice` (`MaxBuckets: 1`, `DefaultPlacement` empty), zonegroup `APIName "ceph-objectstore"`, one period: CreateBucket with `LocationConstraint "other"` → `ErrInvalidLocationConstraint` and the message; with no period and `"other"` → `ErrIllegalLocationConstraint`; placement selection order and `ErrZonegroupPlacementMisconfig` when every default is empty; `PlacementTags` refusing → `ErrAccessDenied`; second bucket → `ErrTooManyBuckets`; `MaxBuckets: -1` → `ErrAccessDenied`; re-create by the owner → `Existed` true and nil error; by another user → `ErrBucketAlreadyExists`; changed placement → the "Cannot modify existing bucket's placement rule" message; anonymous → `ErrAccessDenied`; tenant mismatch → `ErrAccessDenied`. DeleteBucket: missing → `ErrNoSuchBucket`; non-empty → `ErrBucketNotEmpty`. StatBucket and GetBucketLocation happy paths and `APIName` fallbacks. `LogUsage`: owner is the bucket owner when a bucket exists, `"-"` on 404, payer on requester-pays, nothing for a system identity.

`internal/s3/bucketname_test.go`: a `DescribeTable` over `valid_s3_bucket_name`'s rules (`ab` too short; 64 chars too long strict, fine relaxed; `Bucket` uppercase strict-invalid; `-abc`, `abc-`, `a..b`, `a.-b`, `a-.b` strict-invalid, `a.b` valid; `a_b` relaxed only; `192.168.1.1` invalid; `_abc` relaxed valid). `internal/s3/bucket_test.go` on `memstore`: `PUT /plain` → 200 empty body and a bucket owned by the identity with `user.rgw.acl` decoding to a FULL_CONTROL owner grant; `PUT /plain` again → 200; by another identity → 409 `BucketAlreadyExists`; `PUT /ab` → 400 `InvalidBucketName`; body `<CreateBucketConfiguration><LocationConstraint>zz</LocationConstraint></CreateBucketConfiguration>` → 400 `InvalidLocationConstraint` with radosgw's message; `x-amz-bucket-object-lock-enabled: maybe` → 400; `DELETE /plain` → 204 then `HEAD /plain` → 404; `HEAD /plain` on Squid → `X-RGW-Object-Count: 0`, `X-RGW-Bytes-Used: 0`, `X-RGW-Quota-Max-Buckets: 1000` for the owner and none for another identity; on Tentacle (a `memstore.Config{Release: denc.Tentacle}`) no counts without `?read-stats`, counts with it; `GET /plain?location` → `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">ceph-objectstore</LocationConstraint>` and, on Tentacle, the `x-rgw-bucket-placement-target` header.

- [ ] **Step 3: Write `bucketops.go` (driver)**

```go
package driver

func (s *Store) bucketID() string {
	n := s.nextBucketID.Add(1)
	return fmt.Sprintf("%s.%d.%d", s.zone.Params.ID, s.cluster.InstanceID(), n)
}

func (s *Store) defaultLayout(indexType meta.IndexType) meta.BucketLayout {
	l := meta.NewBucketLayout()
	l.Current.Gen = 0
	l.Current.Layout.Type = indexType
	l.Current.Layout.Normal.HashType = meta.HashMod
	shards := s.zone.Zone.BucketIndexMaxShards
	if s.opts.overrideIndexMaxShards > 0 {
		shards = s.opts.overrideIndexMaxShards
	}
	l.Current.Layout.Normal.NumShards = shards
	if indexType == meta.IndexNormal {
		l.Logs = []meta.LogLayoutGen{meta.LogLayoutFromIndex(0, l.Current)}
	}
	return l
}

func (s *Store) initIndex(ctx context.Context, info *meta.BucketInfo) error {
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.bucketIndexMaxAIO)
	for _, oid := range shardOIDs(info, info.Layout.Current) {
		g.Go(func() error {
			wop := radosclient.NewWriteOp()
			wop.Create(true)
			rgwcls.BucketInitIndex(wop)
			if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
				return fmt.Errorf("initializing index shard %s: %w", oid, err)
			}
			return nil
		})
	}
	return g.Wait()
}

func (s *Store) cleanIndex(ctx context.Context, info *meta.BucketInfo) error {
	pool, err := s.indexPool(ctx, info)
	if err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.bucketIndexMaxAIO)
	for _, oid := range shardOIDs(info, info.Layout.Current) {
		g.Go(func() error {
			wop := radosclient.NewWriteOp()
			wop.Remove()
			if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil && !errors.Is(err, radosclient.ErrNotFound) {
				return fmt.Errorf("removing index shard %s: %w", oid, err)
			}
			return nil
		})
	}
	return g.Wait()
}

const maxCreateRetries = 20 // MAX_CREATE_RETRIES, rgw_rados.cc:2371

// CreateBucket is RGWRados::create_bucket, put_linked_bucket_info and
// RadosBucket::create (rgw_rados.cc:2354-2458, :9039-9075; rgw_sal_rados.cc:159-231).
func (s *Store) CreateBucket(ctx context.Context, p op.CreateBucketParams) (*op.BucketRecord, error) {
	pi, ok := s.zone.Params.PlacementPools[p.Placement.Name]
	if !ok {
		return nil, fmt.Errorf("placement %q: %w", p.Placement.Name, op.ErrInvalidLocationConstraint)
	}
	if _, err := s.Placement(p.Placement); err != nil {
		return nil, err
	}
	for range maxCreateRetries {
		id := s.bucketID()
		info := meta.NewBucketInfo()
		info.Bucket = meta.BucketID{Tenant: p.Tenant, Name: p.Name, Marker: id, ID: id}
		info.Owner, info.Zonegroup, info.PlacementRule = p.Owner, p.Zonegroup, p.Placement
		info.HasInstanceObj = true
		info.CreationTime = meta.Time{Time: s.now()}
		info.Quota = p.Quota
		info.Layout = s.defaultLayout(meta.IndexType(pi.IndexType))
		indexless := meta.IndexType(pi.IndexType) == meta.IndexIndexless
		if !indexless {
			if err := s.initIndex(ctx, &info); err != nil {
				return nil, err
			}
		}
		v := &objv{write: newWriteVersion()}
		err := s.writeInstance(ctx, &info, p.Attrs, true, info.CreationTime.Time, v)
		var epv *objv
		if err == nil {
			ep := meta.NewBucketEntryPoint()
			ep.Bucket, ep.Owner, ep.CreationTime, ep.Linked = info.Bucket, p.Owner, info.CreationTime, true
			epv = &objv{write: newWriteVersion()}
			err = s.writeEntryPoint(ctx, ep, nil, true, info.CreationTime.Time, epv)
		}
		if errors.Is(err, op.ErrConcurrentModification) || errors.Is(err, op.ErrBucketAlreadyExists) {
			existing, rerr := s.GetBucket(ctx, p.Tenant, p.Name)
			if errors.Is(rerr, op.ErrNoSuchBucket) {
				continue // raced with a removal; try again (:2425-2429)
			}
			if rerr != nil {
				return nil, rerr
			}
			if existing.Info.Bucket.ID != id {
				if !indexless {
					if cerr := s.cleanIndex(ctx, &info); cerr != nil {
						slog.WarnContext(ctx, "could not remove the losing bucket index", slog.Any("error", cerr))
					}
				}
				if rerr := s.removeInstance(ctx, info.Bucket, nil); rerr != nil && !errors.Is(rerr, op.ErrNoSuchBucket) {
					slog.WarnContext(ctx, "could not remove the losing bucket instance", slog.Any("error", rerr))
				}
			}
			return existing, fmt.Errorf("bucket %s: %w", p.Name, op.ErrBucketAlreadyExists)
		}
		if err != nil {
			return nil, err
		}
		rec := &op.BucketRecord{Info: info, Attrs: p.Attrs, Version: v.read, EPVersion: epv.read, Mtime: info.CreationTime.Time}
		rec.EntryPoint = meta.NewBucketEntryPoint()
		rec.EntryPoint.Bucket, rec.EntryPoint.Owner, rec.EntryPoint.CreationTime, rec.EntryPoint.Linked = info.Bucket, p.Owner, info.CreationTime, true
		if err := s.linkBucket(ctx, p.Owner, info.Bucket, info.CreationTime.Time); err != nil {
			if uerr := s.unlinkBucket(ctx, p.Owner, info.Bucket); uerr != nil {
				slog.WarnContext(ctx, "failed to unlink bucket after a link failure", slog.Any("error", uerr))
			}
			return nil, op.FromRADOS(err, op.ScopeBucket)
		}
		if _, err := s.readEntryPoint(ctx, p.Tenant, p.Name); errors.Is(err, op.ErrNoSuchBucket) {
			slog.WarnContext(ctx, "the bucket entry point was deleted by a concurrent request; unlinking", slog.String("bucket", p.Name))
			if uerr := s.unlinkBucket(ctx, p.Owner, info.Bucket); uerr != nil {
				slog.WarnContext(ctx, "failed to unlink bucket", slog.Any("error", uerr))
			}
		}
		return rec, nil
	}
	return nil, fmt.Errorf("%w: could not create bucket %s, continuously raced with bucket creation and removal", op.ErrNoSuchBucket, p.Name)
}

// checkBucketEmpty is RGWRados::check_bucket_empty (rgw_rados.cc:5186-5231).
func (s *Store) checkBucketEmpty(ctx context.Context, rec *op.BucketRecord) error {
	if rec.Info.Zonegroup != s.zone.ZoneGroup.ID {
		return nil
	}
	var marker rgwcls.ObjKey
	for {
		entries, truncated, last, err := s.listUnordered(ctx, rec, marker, "", 1000, true)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if k, ok := meta.ParseIndexKeyName(e.Key.Name); ok && k.NS == "" {
				return fmt.Errorf("bucket %s: %w", rec.Info.Bucket.Name, op.ErrBucketNotEmpty)
			}
		}
		if !truncated {
			return nil
		}
		marker = last
	}
}

// DeleteBucket is RadosBucket::remove (rgw_sal_rados.cc:350-468) and
// RGWRados::delete_bucket (rgw_rados.cc:5238-5302) for delete_children=false.
func (s *Store) DeleteBucket(ctx context.Context, rec *op.BucketRecord) error {
	own := rec.Info.Zonegroup == s.zone.ZoneGroup.ID
	if own {
		if _, err := s.syncOwnerStats(ctx, rec.Info.Owner, &rec.Info); err != nil {
			slog.WarnContext(ctx, "failed to sync owner stats before bucket delete", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
		}
		if err := s.checkBucketEmpty(ctx, rec); err != nil {
			return err
		}
		// radosgw runs abort_multiparts here, under own_bucket after the
		// emptiness check (rgw_sal_rados.cc:395-400 at v19.2.6, :413-418 at
		// v20.2.4). rgw-go does not abort uploads yet, so a radosgw-created
		// in-flight upload on a shared zone is orphaned by this delete.
	}
	epv := &objv{read: rec.EPVersion}
	removeEP := true
	if epv.read.Ver == 0 {
		e, err := s.readEntryPoint(ctx, rec.Info.Bucket.Tenant, rec.Info.Bucket.Name)
		switch {
		case err != nil:
			removeEP = false // unknown state: leave it (rgw_rados.cc:5264-5276)
		case rec.Info.Bucket.ID != "" && e.ep.Bucket.ID != rec.Info.Bucket.ID:
			removeEP = false
		default:
			epv.read = e.v.read
		}
	}
	if removeEP {
		if err := s.removeEntryPoint(ctx, rec.Info.Bucket.Tenant, rec.Info.Bucket.Name, epv); err != nil && !errors.Is(err, op.ErrConcurrentModification) {
			return err
		}
	}
	if err := s.removeInstance(ctx, rec.Info.Bucket, nil); err != nil && !errors.Is(err, op.ErrNoSuchBucket) {
		return err
	}
	if rec.Info.Layout.Current.Layout.Type == meta.IndexNormal {
		if err := s.cleanIndex(ctx, &rec.Info); err != nil {
			slog.WarnContext(ctx, "could not remove bucket index", slog.String("bucket", rec.Info.Bucket.Name), slog.Any("error", err))
		}
	}
	if err := s.unlinkBucket(ctx, rec.Info.Owner, rec.Info.Bucket); err != nil {
		return op.FromRADOS(err, op.ScopeBucket)
	}
	return nil
}
```

`s.nextBucketID` is an `atomic.Uint64` on `Store`; `s.opts.overrideIndexMaxShards` is `rgw_override_bucket_index_max_shards` (Task 12; read inline until then). P Task 7 Step 7 inserts `s.abortMultiparts(ctx, rec)` at the comment in the `own` branch of `DeleteBucket`. radosgw sends `ECANCELED` from the entry-point removal through `RadosBucket::remove` up to `RGWDeleteBucket`, which turns it into success after the driver already unlinked ([rgw_op.cc:3793-3797](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3793-L3797)) — the driver here keeps the same order (unlink runs last) by swallowing the race before unlinking.

- [ ] **Step 4: Write the ops, `LogUsage`, `ValidBucketName` and the handlers**

`internal/op/bucketops.go` implements the four ops as specified above; `parseValueAndBound` ([rgw_op.h:2633-2660](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2633-L2660)) lives here for Task 8 too. `internal/op/usagelog.go` implements `LogUsage`. `internal/s3/bucketname.go` ports `valid_s3_bucket_name` character by character, using G's `looksLikeIPAddress` from `request.go`. `internal/s3/bucket.go` fills `bucketHandlers()`:

```go
func bucketHandlers() map[string]HandlerFunc {
	return map[string]HandlerFunc{
		"create_bucket":       createBucket,
		"delete_bucket":       deleteBucket,
		"stat_bucket":         statBucket,
		"get_bucket_location": getBucketLocation,
	}
}
```

Task 8 adds `list_bucket` and `list_bucket_v2` to `bucketHandlers`, Task 11 the acl, policy and tagging routes.

`createBucket` reads the body with `io.LimitReader(r.Body, maxPutParamSize+1)`, parses it with `encoding/xml` into `struct{ XMLName xml.Name `xml:"CreateBucketConfiguration"`; LocationConstraint *string }` when non-empty, splits `"zg:placement"`, sets `Placement.StorageClass` from `x-amz-storage-class`, validates `x-amz-bucket-object-lock-enabled`, builds the ACL through the `authz.Evaluator` reachable as `r.Env.Authz.(*authz.Evaluator)` with the resolver `userResolver(r)` — a three-line helper in `bucket.go`, `func userResolver(r *op.Request) authz.UserResolver { return authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts} }`, which compiles in wave 1 because A Task 1 gives `op.AccountStore` the `AccountName` method `authz.AccountLookup` wants, and which W Task 11 and P Task 9 reuse — if `Env.Authz` is Z's evaluator; when it is G's `OwnerOnly` stub (before Z lands) fall back to `acl.DefaultPolicy(owner, displayName)` — and runs `op.Run`; `internal/op/listbuckets.go` (G Task 4): `ListBuckets.Complete` becomes `LogUsage(ctx, r, "list_buckets")`, since radosgw's `rgw_log_op` runs `log_usage` for every op when the usage log is enabled and keys a bucketless op under the user with bucket `"-"` ([rgw_log.cc:558-559](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L558-L559), [:195-224](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L195-L224) at v19.2.6); `statBucket` sets `ReadStats` = `release < Tentacle || r.Query.Has("read-stats")` and writes the headers as specified; `getBucketLocation` writes the XML and, on Tentacle, the header.

- [ ] **Step 5: Run the suites; expected PASS; `make check`; commit**

Three commits: `feat(driver): create and delete buckets with radosgw's index, instance, entry point and link sequence`, `feat(op): CreateBucket, DeleteBucket, StatBucket, GetBucketLocation and the usage log entry`, `feat(s3): bucket create, delete, head and location handlers`. Draft PR `feat: bucket create, delete, head and location (unit M task 7)`.

### Task 8: ListObjects v1 and v2 with pending-entry reconciliation

**Files:**
- Create: `internal/driver/list.go`, `internal/driver/list_test.go`, `internal/op/listobjects.go`, `internal/op/listobjects_test.go`, `internal/s3/listobjects.go`, `internal/s3/listobjects_test.go`
- Modify: `internal/op/bucket.go` (`ListObjectsParams.AllowUnordered`), `internal/op/opfakes/` (regenerate), `internal/memstore/bucket.go` (honour `AllowUnordered`: no common prefixes, reject with `op.ErrInvalidArgument` when a delimiter is given), `internal/meta/objkey.go` (`ParseIndexKeyName`), `internal/testutil/fakerados/cls_rgw.go` (`bucket_list` with entries, `dir_suggest_changes`, `guard_bucket_resharding`), `internal/s3/bucket.go` (register `list_bucket`, `list_bucket_v2`)

**Interfaces:**
- Consumes: `cls/rgw` (`BucketList`, `ListOp`, `ListRet`, `DirEntry`, `DirEntryMeta`, `PendingEntry`, `EntryVer`, `Suggestion`, `SuggestChanges`, `SuggestRemove`, `SuggestUpdate`, `GuardBucketResharding`, `FlagCurrent`, `FlagDeleteMarker`, `FlagCommonPrefix`, `CategoryMain`), Task 6's `indexPool` and `shardOIDs`, `meta.IndexShard`, `meta.ObjKey.IndexKeyName`, `acl.DecodePolicy`, `meta.AttrACL`, `meta.AttrETag`, `meta.AttrContentType`, `meta.AttrStorageClass`, `op.ObjectStore.StatObject` (through `headStater`), `op.ObjectEntry`, `op.ListObjectsParams`, `op.ListObjectsResult`, `op.ErrMethodNotAllowed`, `op.ErrInvalidArgument`, `op.ErrInvalidBucketState`, `cephconf.Options` (`rgw_list_bucket_min_readahead`, `rgw_max_listing_results`, `rgw_bucket_index_max_aio`); G's `writeChunkedXML` (Task 4), the chunked framing both listings share with radosgw; G's `xmltext.Text` (Task 4), the type of every string field in the listing documents.
- Produces:

```go
package meta

// ParseIndexKeyName is rgw_obj_key::parse_raw_oid (rgw_obj_types.h:285-310):
// the inverse of ObjKey.IndexKeyName. ok is false for a malformed
// namespaced name. The instance is not carried by the name; the caller takes
// it from the index key's instance field.
func ParseIndexKeyName(name string) (k ObjKey, ok bool)
```

```go
package op

type ListObjectsParams struct {
	Prefix, Delimiter, Marker string
	MaxKeys                   int
	NS                        string
	ListVersions              bool
	AllowUnordered            bool // radosgw's non-standard allow-unordered
}
```

```go
package driver

// headStater is the one ObjectStore method the listing's disk check needs.
type headStater interface {
	StatObject(ctx context.Context, rec *op.BucketRecord, key meta.ObjKey) (*op.ObjectState, error)
}

// shardListing is one shard's bucket_list reply with its cursor.
type listCursor struct {
	entries   []rgwcls.DirEntry
	truncated bool
	filtered  bool
}

// calcPerShard is calc_ordered_bucket_list_per_shard (rgw_rados.cc:9578-9611).
func calcPerShard(numEntries, numShards uint32) uint32

// listOrdered is cls_bucket_list_ordered (rgw_rados.cc:9614-9945).
func (s *Store) listOrdered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix, delimiter string, numEntries uint32, listVersions bool, attempt uint16) (entries []rgwcls.DirEntry, truncated, filtered bool, last rgwcls.ObjKey, err error)

// listUnordered is cls_bucket_list_unordered (:9965-10155);
// checkBucketEmpty calls it with prefix "" and listVersions true.
func (s *Store) listUnordered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix string, numEntries uint32, listVersions bool) (entries []rgwcls.DirEntry, truncated bool, last rgwcls.ObjKey, err error)

// checkDiskState is check_disk_state (:10323-10475): reconcile one index
// entry with its head through the object store, and record the suggestion.
// It returns false when the entry is to be dropped from the listing.
func (s *Store) checkDiskState(ctx context.Context, rec *op.BucketRecord, indexVer rgwcls.EntryVer, ent *rgwcls.DirEntry, updates map[string][]rgwcls.Suggestion) (keep bool, err error)

// suggest sends the per-shard suggestions blindly (an async write per
// shard, bounded by rgw_bucket_index_max_aio); on Tentacle each is guarded.
func (s *Store) suggest(ctx context.Context, pool radosclient.Pool, updates map[string][]rgwcls.Suggestion)

// ListObjects is RGWRados::Bucket::List::list_objects_ordered and
// list_objects_unordered (:1802-2330).
func (s *Store) ListObjects(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error)
```

```go
package op

// ListObjects is RGWListBucket for S3 v1 and v2 (rgw_op.cc:3033-3121;
// rgw_rest_s3.cc:1710-1771).
type ListObjects struct {
	V2                bool
	Prefix, Delimiter string
	Marker            string // v1 marker, or v2's continuation-token else start-after
	MaxKeys           string // raw max-keys, bounded in Execute
	EncodingType      string
	AllowUnordered    bool
	FetchOwner        bool
	StartAfter        string
	HasStartAfter     bool
	ContinuationToken string
	HasToken          bool

	Max    int // the bounded max-keys
	Result ListObjectsResult
}
```

```go
package s3

// listBucket and listBucketV2 are RGWListBucket_ObjStore_S3 and _S3v2.
func listBucket(ctx context.Context, w http.ResponseWriter, r *op.Request) error
func listBucketV2(ctx context.Context, w http.ResponseWriter, r *op.Request) error
```

The driver's `ListObjects` transcribes the four C++ functions with these bindings: `max = clamp(p.MaxKeys, 0, 25000)`, `readAhead = max(rgw_list_bucket_min_readahead, max)`; the marker and prefix become index key names in `p.NS` (`meta.ObjKey{Name, NS}.IndexKeyName()`); with a delimiter a marker inside a common prefix is advanced to `prefix + "\xFF"`; up to `SOFT_MAX_ATTEMPTS = 8` rounds, each a `listOrdered` for `readAhead + 1 - count` with the attempt as the expansion factor, stopping when the marker stops moving; per entry: `meta.ParseIndexKeyName` (a malformed name is skipped), non-visible entries skipped unless versions are listed, namespace enforcement with the `_^\xFF`/`_\xFF` region skips, `NextMarker` tracks the last entry or prefix while `count < max`, the prefix filter, common prefixes (one count each; `cls_filtered` is always true on Squid and Tentacle OSDs, so the client-side rollup is kept only as the fallback the C++ keeps), `count >= max` → truncated; the round loop ends when not truncated or at least half of `max` was collected, or after eight rounds with anything. `listUnordered` runs `while truncated && count <= max` with `readAhead = max + min(max, 100)`, starting at the shard the marker hashes to (multipart names hashed on their upload key, `parse_index_hash_source`), and never produces common prefixes. `listOrdered` requests `calcPerShard` entries per shard (scaled `1 << (attempt-1)`, capped at `num_entries` above factor 11), runs one `bucket_list` per shard with at most `rgw_bucket_index_max_aio` in flight, merges by name, reconciles entries that are not `exists`, are not a delete marker or common prefix, or have a pending map, drops the ones whose head is gone (their suggestion is `SuggestRemove`), stops at the first exhausted truncated shard, and sends the suggestions. `checkDiskState`: `Exists == false` → `ent.Ver = indexVer`, a `SuggestRemove`, drop; otherwise `Meta.Size = st.Size`, `Meta.AccountedSize = st.Compression.OrigSize` when compressed else `st.Size`, `Mtime`, `ETag`, `ContentType`, `StorageClass`, the owner and display name from the head's `user.rgw.acl`, `Appendable` from `user.rgw.append_part_num`, `Category = Main`, `Ver = {Pool: data pool id, Epoch: st.Epoch}`, `Tag = st.WriteTag`, `Exists = true`, `PendingMap = nil`, a `SuggestUpdate`, keep; `op.ErrNotImplemented` from the stater keeps the entry unchanged with no suggestion (M-D6); the `Log` flag is `s.zone.Zone.LogData`. The multipart-part `delete_obj_index` sweep ([:10413-10429](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L10413-L10429); v20.2.4 [:11337-11353](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11337-L11353)) is W Task 6 Steps 5-7's `deleteObjIndex`, inserted at the comment the code below leaves; it runs only for a head that exists, because the `!exists` branch returns first (:10362-10378), so until W lands a listing that reconciles an unlanded multipart completion leaves the part entries for `radosgw-admin bucket check` to report. `suggest`: per shard a `WriteOp` of `SuggestChanges` alone on Squid, or `AssertExists(); GuardBucketResharding(); SuggestChanges()` on Tentacle (v20.2.4 [`rgw_rados.cc:10822-10824`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L10822-L10824), [`:11060-11062`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L11060-L11062)), run in a goroutine under `context.WithoutCancel(ctx)` with a 30 s timeout and a `semaphore.Weighted` of `rgw_bucket_index_max_aio`, errors at debug level.

The result maps `rgwcls.DirEntry` to `op.ObjectEntry{Key: parsed key with the index key's instance, Size: Meta.AccountedSize (what radosgw renders as Size), Mtime, ETag, Owner: meta.ParseOwner(Meta.Owner), OwnerDisplayName, StorageClass: canonical (empty → STANDARD), IsLatest: Flags&FlagCurrent, DeleteMarker: Flags&FlagDeleteMarker, Exists}`; `NextMarker` is the index key's raw name, exactly as radosgw dumps `next_marker.name` ([rgw_rest_s3.cc:1963-1965](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1963-L1965)), including the `__` escaping of a leading underscore — a radosgw quirk Task 13 confirms on the cluster before it is recorded.

- [ ] **Step 1: Write the failing driver specs**

`internal/driver/list_test.go`, package `driver_test`; `seedIndexEntry(c, indexPool, shardOID, ent rgwcls.DirEntry)` writes `encode(ent)` under `Omap[ent.Key.Name]` (the index omap key is the raw index name; a versioned key appends the instance, out of scope) and keeps the shard header in `OmapHdr`; the spec's `seedEntry(e)` calls it on `".dir.<id>.<shardOf(e.Key.Name)>"` with `shardOf(name) = meta.IndexShard(name, 2)`, and `keys(entries)` collects `Key.Name`s; `stater` is a counterfeiter fake of `driver.HeadStater` (export the interface under that name for the fake; generate into `internal/driver/driverfakes`) that the spec wires with `driver.SetHeadStaterForTest(s, stater)` (a test-only setter in `export_test.go`). The store is set up as in Task 6, plus the rgw class:

```go
var _ = Describe("ListObjects", func() {
	// bucket "plain" with 2 shards seeded through seedBucket; entries are
	// seeded into ".dir.<id>.<shard>" by meta.IndexShard(name, 2)
	entry := func(name string, size uint64) rgwcls.DirEntry {
		e := rgwcls.NewDirEntry()
		e.Key = rgwcls.ObjKey{Name: name}
		e.Exists = true
		e.Meta = rgwcls.DirEntryMeta{Category: rgwcls.CategoryMain, Size: size, AccountedSize: size, Mtime: mtime, ETag: "e-" + name, Owner: "alice", OwnerDisplayName: "Alice", StorageClass: "STANDARD"}
		return e
	}
	It("lists in key order across shards with common prefixes and a bounded page", func(ctx SpecContext) {
		for _, n := range []string{"a", "dir/x", "dir/y", "b", "c"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"a", "b", "c"}))
		Expect(res.CommonPrefixes).To(BeEmpty())
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal("c"))
		res, err = s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 3, Marker: "c"})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(BeEmpty())
		Expect(res.CommonPrefixes).To(Equal([]string{"dir/"}), "the delimiter is applied by the class")
		Expect(res.Truncated).To(BeFalse())
	})
	It("fast-forwards a marker inside a common prefix", func(ctx SpecContext) {
		for _, n := range []string{"dir/x", "dir/y", "e"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{Delimiter: "/", MaxKeys: 10, Marker: "dir/x"})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"e"}), "rgw_rados.cc:1844-1854: the marker becomes dir/\\xFF")
		Expect(res.CommonPrefixes).To(BeEmpty())
	})
	It("lists an object whose name starts with an underscore and skips the multipart namespace", func(ctx SpecContext) {
		seedEntry(entry("__under", 1))          // index name of "_under"
		seedEntry(entry("_multipart_up.2~x.1", 1)) // a part in the multipart namespace
		seedEntry(entry("z", 1))
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"_under", "z"}), "parse_raw_oid unescapes, rgw_obj_types.h:293-296; the namespaced entry is skipped, rgw_rados.cc:1926-1988")
		Expect(res.Entries[0].Key.NS).To(BeEmpty())
	})
	It("reconciles a pending entry against its head and drops one whose head is gone", func(ctx SpecContext) {
		gone := entry("gone", 1)
		gone.PendingMap = []rgwcls.PendingEntry{{Tag: "tx1", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime, Op: uint8(rgwcls.OpAdd)}}}
		seedEntry(gone)
		stale := entry("stale", 1)
		stale.PendingMap = gone.PendingMap
		seedEntry(stale)
		stater.StatObjectStub = func(_ context.Context, _ *op.BucketRecord, k meta.ObjKey) (*op.ObjectState, error) {
			if k.Name == "gone" {
				return &op.ObjectState{Key: k, Exists: false}, nil
			}
			return &op.ObjectState{Key: k, Exists: true, Size: 77, Mtime: mtime, Epoch: 9, ETag: "fresh", WriteTag: "tx2",
				Attrs: map[string][]byte{meta.AttrACL: encode(acl.Policy{Owner: acl.Owner{ID: "alice", DisplayName: "Alice"}})}}, nil
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"stale"}), "rgw_rados.cc:10361-10377 drops the missing head")
		Expect(res.Entries[0].Size).To(BeEquivalentTo(77), "the head's size wins, :10440-10447")
		Expect(res.Entries[0].ETag).To(Equal("fresh"))
		shard := ".dir." + rec.Info.Bucket.ID + "." + fmt.Sprint(shardOf("gone"))
		Eventually(func() []rgwcls.Suggestion { return c.Suggestions("ceph-objectstore.rgw.buckets.index", "", shard) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(ContainElement(HaveField("Op", rgwcls.SuggestRemove)))
		Expect(c.Suggestions("ceph-objectstore.rgw.buckets.index", "", shard)).To(ContainElement(HaveField("Log", false)), "log_data is false in a single zone")
	})
	It("sends suggestions unguarded on Squid and guarded on Tentacle", func(ctx SpecContext) {
		// two stores over two clusters: release Squid and Tentacle (driver.Options{Release: &t});
		// one pending entry whose head is gone; assert c.LastWrite(index pool, shard).Steps() has
		// [ExecStep dir_suggest_changes] on Squid and
		// [AssertExistsStep, ExecStep guard_bucket_resharding, ExecStep dir_suggest_changes] on Tentacle
		// (v19.2.6 rgw_rados.cc:9899-9912; v20.2.4 :10819-10832; docs/ceph-upstream-bugs.md #81000)
	})
	It("keeps an entry and sends nothing when the object store is not implemented yet", func(ctx SpecContext) {
		pending := entry("p", 1)
		pending.PendingMap = []rgwcls.PendingEntry{{Tag: "t", Info: rgwcls.PendingInfo{State: rgwcls.PendingModify, Timestamp: mtime}}}
		seedEntry(pending)
		stater.StatObjectReturns(nil, op.ErrNotImplemented)
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 10})
		Expect(err).NotTo(HaveOccurred())
		Expect(keys(res.Entries)).To(Equal([]string{"p"}))
		Consistently(func() int { return c.Writes("ceph-objectstore.rgw.buckets.index", "", ".dir."+rec.Info.Bucket.ID+"."+fmt.Sprint(shardOf("p"))) }).WithTimeout(50 * time.Millisecond).Should(BeZero())
	})
	It("lists unordered by shard without common prefixes", func(ctx SpecContext) {
		for _, n := range []string{"a", "b", "c", "d"} {
			seedEntry(entry(n, 1))
		}
		res, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 3, AllowUnordered: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Entries).To(HaveLen(3))
		Expect(res.Truncated).To(BeTrue())
		Expect(res.NextMarker).To(Equal(res.Entries[2].Key.Name))
		res2, err := s.ListObjects(ctx, rec, op.ListObjectsParams{MaxKeys: 3, AllowUnordered: true, Marker: res.NextMarker})
		Expect(err).NotTo(HaveOccurred())
		Expect(append(keys(res.Entries), keys(res2.Entries)...)).To(ConsistOf("a", "b", "c", "d"))
	})
	It("refuses an index with zero shards as radosgw does", func(ctx SpecContext) {
		// a fake whose shardOIDs is empty is impossible (0 shards is one object), so drive the
		// -ERR_INVALID_BUCKET_STATE path by a per-shard count of 0: calcPerShard(0, 1) is 8, so
		// this is pinned on calcPerShard instead:
		Expect(calcPerShard(0, 0)).To(BeZero(), "rgw_rados.cc:9581-9585")
		Expect(calcPerShard(1000, 1)).To(BeEquivalentTo(1001))
		Expect(calcPerShard(8, 64)).To(BeEquivalentTo(8), "min_read")
	})
})
```

(`calcPerShard` is unexported: that spec lives in a `package driver` file.) `fakerados` gains `Suggestions(pool, ns, oid) []rgwcls.Suggestion` (decoded by the rgw emulator), `Writes(pool, ns, oid) int` and `LastWrite(pool, ns, oid) []radosclient.Step`.

- [ ] **Step 2: Write the failing op and handler specs**

`internal/op/listobjects_test.go` on `memstore` (G's `ListObjects` semantics): `max-keys` bounding (`""` → 1000; `"0"` → 0 entries, `IsTruncated` false; `"5000"` with `rgw_max_listing_results=1000` → 1000; `"x"` → `op.ErrInvalidArgument`); unordered with a delimiter → `op.ErrInvalidArgument`; v2 `continuation-token` beats `start-after` for the marker; a missing bucket → `op.ErrNoSuchBucket`; Tentacle release with an `Indexless` layout → `op.ErrMethodNotAllowed` with message `Indexless buckets cannot be listed`; Squid lists it.

`internal/s3/listobjects_test.go` on `memstore` with three objects `a`, `dir/x`, `_under` (`newHandler` from G's Task 4 specs; `get` serves one request through that handler, and `srv` is an `httptest.Server` over the same handler, for the framing a client sees):

```go
It("renders v1 in radosgw's element order", func() {
	rec := get("/plain?delimiter=/&max-keys=1")
	Expect(rec.Code).To(Equal(200))
	Expect(rec.Body.String()).To(Equal(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` +
		`<Name>plain</Name><Prefix></Prefix><MaxKeys>1</MaxKeys><Delimiter>/</Delimiter><IsTruncated>true</IsTruncated>` +
		`<Contents><Key>_under</Key><LastModified>2026-09-27T01:02:03.456Z</LastModified><ETag>&quot;e1&quot;</ETag><Size>1</Size><StorageClass>STANDARD</StorageClass>` +
		`<Owner><ID>alice</ID><DisplayName>Alice</DisplayName></Owner><Type>Normal</Type></Contents>` +
		`<Marker></Marker><NextMarker>__under</NextMarker></ListBucketResult>`))
})
It("renders common prefixes before contents and url-encodes when asked", func() {
	rec := get("/plain?delimiter=/&encoding-type=url&prefix=")
	Expect(rec.Body.String()).To(ContainSubstring(`<EncodingType>url</EncodingType><Name>plain</Name>`))
	Expect(rec.Body.String()).To(MatchRegexp(`<CommonPrefixes><Prefix>dir/</Prefix></CommonPrefixes><Contents>`), "prefixes come from send_common_response, before Contents")
})
It("renders v2 with KeyCount, the tokens and Owner only when fetched", func() {
	rec := get("/plain?list-type=2&max-keys=2&start-after=_under&fetch-owner=true")
	Expect(rec.Body.String()).To(ContainSubstring(`<Contents><Key>a</Key>`))
	Expect(rec.Body.String()).To(ContainSubstring(`<Owner><ID>alice</ID>`))
	Expect(rec.Body.String()).To(MatchRegexp(`<NextContinuationToken>dir/x</NextContinuationToken><KeyCount>2</KeyCount><StartAfter>_under</StartAfter></ListBucketResult>$`))
	rec = get("/plain?list-type=2&continuation-token=a")
	Expect(rec.Body.String()).NotTo(ContainSubstring("<Owner>"))
	Expect(rec.Body.String()).To(ContainSubstring(`<ContinuationToken>a</ContinuationToken>`))
})
It("answers 400 InvalidArgument for a non-numeric max-keys and 405 for an unordered listing with a delimiter", func() {
	Expect(get("/plain?max-keys=x").Code).To(Equal(400))
	Expect(get("/plain?allow-unordered=true&delimiter=/").Code).To(Equal(400))
})
It("frames v1 and v2 chunked as radosgw's send_response does: no Content-Length and no Accept-Ranges", func() {
	for _, target := range []string{"/plain", "/plain?list-type=2"} {
		resp, err := http.Get(srv.URL + target)
		Expect(err).NotTo(HaveOccurred())
		b, err := io.ReadAll(resp.Body)
		Expect(resp.Body.Close()).To(Succeed())
		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode).To(Equal(200), target)
		Expect(resp.TransferEncoding).To(Equal([]string{"chunked"}), "end_header(..., CHUNKED_TRANSFER_ENCODING), rgw_rest_s3.cc:1906 and :2062")
		Expect(resp.ContentLength).To(BeEquivalentTo(-1), target)
		Expect(resp.Header.Get("Accept-Ranges")).To(BeEmpty(), "no Content-Length, so no dump_content_length")
		Expect(resp.Header.Get("Content-Type")).To(Equal("application/xml"), target)
		Expect(string(b)).To(HavePrefix(`<?xml version="1.0" encoding="UTF-8"?><ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`), target)
	}
})
It("answers a failed listing with an ordinary error document", func() {
	resp, err := http.Get(srv.URL + "/plain?max-keys=x")
	Expect(err).NotTo(HaveOccurred())
	Expect(resp.Body.Close()).To(Succeed())
	Expect(resp.StatusCode).To(Equal(400))
	Expect(resp.TransferEncoding).To(BeEmpty(), "end_header's error branch sends the document with a Content-Length, rgw_rest.cc:620-624")
	Expect(resp.Header.Get("Accept-Ranges")).To(Equal("bytes"))
})
```

The ETag's quotes are `&quot;` because radosgw writes the element with `dump_format("ETag", "\"%s\"")`, whose text `xml_stream_escaper` escapes ([rgw_rest_s3.cc:1942](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1942) and [:2086](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2086) at v19.2.6, :2053 and [:2197](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2197) at v20.2.4; [src/common/escape.cc:134-169](https://github.com/ceph/ceph/blob/v19.2.6/src/common/escape.cc#L134-L169)); every string field of the listing structs is an `xmltext.Text` (G Task 4), so `encoding/xml`'s own `&#34;` never appears.

- [ ] **Step 3: Write `meta.ParseIndexKeyName`, `list.go`, the op and the handlers**

`internal/meta/objkey.go`:

```go
// ParseIndexKeyName is rgw_obj_key::parse_raw_oid (rgw_obj_types.h:285-310).
func ParseIndexKeyName(name string) (ObjKey, bool) {
	if name == "" || name[0] != '_' {
		return ObjKey{Name: name}, true
	}
	if len(name) >= 2 && name[1] == '_' {
		return ObjKey{Name: name[1:]}, true
	}
	if len(name) < 3 {
		return ObjKey{}, false
	}
	pos := strings.IndexByte(name[2:], '_')
	if pos < 0 {
		return ObjKey{}, false
	}
	pos += 2
	ns, instance, _ := strings.Cut(name[1:pos], ":") // parse_ns_field: "<ns>:<instance>"
	return ObjKey{Name: name[pos+1:], Instance: instance, NS: ns}, true
}
```

`internal/driver/list.go`:

```go
package driver

const (
	listAbsoluteMax  = 25000 // bucket_list_objects_absolute_max, rgw_rados.h:1018
	listSoftAttempts = 8     // SOFT_MAX_ATTEMPTS, rgw_rados.cc:1859
	afterDelim       = "\xFF" // cls_rgw_after_delim, cls_rgw_types.h:139-142
	multipartNS      = "multipart"
)

func calcPerShard(numEntries, numShards uint32) uint32 {
	if numShards == 0 {
		return 0
	}
	const minRead = 8
	n, s := float64(numEntries), float64(numShards)
	calc := 1 + uint32(n/s+math.Sqrt(2*n*math.Log(s)/s))
	return max(minRead, calc)
}

// indexKey renders an object name in ns as its index key name.
func indexKey(name, ns string) string { return meta.ObjKey{Name: name, NS: ns}.IndexKeyName() }

func (s *Store) ListObjects(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	if p.AllowUnordered {
		if p.Delimiter != "" {
			return op.ListObjectsResult{}, fmt.Errorf("%w: unordered bucket listing requested with a delimiter", op.ErrInvalidArgument)
		}
		return s.listObjectsUnordered(ctx, rec, p)
	}
	return s.listObjectsOrdered(ctx, rec, p)
}

// listObjectsOrdered is RGWRados::Bucket::List::list_objects_ordered (rgw_rados.cc:1802-2159).
func (s *Store) listObjectsOrdered(ctx context.Context, rec *op.BucketRecord, p op.ListObjectsParams) (op.ListObjectsResult, error) {
	var res op.ListObjectsResult
	maxN := min(max(p.MaxKeys, 0), listAbsoluteMax)
	readAhead := max(s.opts.listMinReadahead, maxN)
	cur := rgwcls.ObjKey{Name: indexKey(p.Marker, p.NS)}
	if p.Marker == "" {
		cur = rgwcls.ObjKey{}
	}
	prefix := indexKey(p.Prefix, p.NS)
	if p.Delimiter != "" {
		if i := strings.Index(cur.Name[min(len(cur.Name), len(prefix)):], p.Delimiter); i >= 0 {
			cur = rgwcls.ObjKey{Name: cur.Name[:len(prefix)+i] + afterDelim}
		}
	}
	prefixes := map[string]bool{}
	count, truncated := 0, true
	var prev rgwcls.ObjKey
attempts:
	for attempt := uint16(1); ; attempt++ {
		if attempt > 1 && prev == cur {
			slog.ErrorContext(ctx, "listing marker failed to make forward progress", slog.String("bucket", rec.Info.Bucket.Name), slog.String("marker", cur.Name))
			break
		}
		prev = cur
		want := uint32(readAhead + 1 - count) //nolint:gosec // bounded by listAbsoluteMax
		entries, trunc, filtered, last, err := s.listOrdered(ctx, rec, cur, prefix, p.Delimiter, want, p.ListVersions, attempt)
		if err != nil {
			return res, err
		}
		truncated, cur = trunc, last
		for i := range entries {
			e := &entries[i]
			key, ok := meta.ParseIndexKeyName(e.Key.Name)
			if !ok {
				slog.WarnContext(ctx, "could not parse index key", slog.String("key", e.Key.Name))
				continue
			}
			key.Instance = e.Key.Instance
			if !p.ListVersions && !e.Exists && e.Flags&(rgwcls.FlagDeleteMarker|rgwcls.FlagCommonPrefix) == 0 { // is_visible
				continue
			}
			if key.NS != p.NS {
				if p.NS != "" {
					truncated = false
					break attempts
				}
				if key.NS != "" {
					skip := rgwcls.ObjKey{Name: "_\xFF"}
					if key.NS[0] < '_' {
						skip = rgwcls.ObjKey{Name: "_^\xFF"}
					}
					if cur.Name < skip.Name {
						cur = skip
						break // another round from the skip marker
					}
				}
				continue
			}
			if count < maxN {
				res.NextMarker = e.Key.Name
			}
			if p.Prefix != "" && !strings.HasPrefix(key.Name, p.Prefix) {
				continue
			}
			if p.Delimiter != "" {
				if i := strings.Index(key.Name[len(p.Prefix):], p.Delimiter); i >= 0 {
					pfx := key.Name
					if !filtered { // an OSD that did not roll up: do it here (rgw_rados.cc:2054-2085)
						pfx = key.Name[:len(p.Prefix)+i+len(p.Delimiter)]
					}
					if !prefixes[pfx] {
						if count >= maxN {
							truncated = true
							break attempts
						}
						prefixes[pfx] = true
						res.CommonPrefixes = append(res.CommonPrefixes, pfx)
						res.NextMarker = pfx
						count++
					}
					continue
				}
			}
			if count >= maxN {
				truncated = true
				break attempts
			}
			res.Entries = append(res.Entries, s.objectEntry(key, e))
			count++
		}
		if !filtered && p.Delimiter != "" {
			if i := strings.Index(cur.Name[min(len(cur.Name), len(prefix)):], p.Delimiter); i >= 0 {
				if skip := cur.Name[:len(prefix)+i] + afterDelim; skip > cur.Name {
					cur = rgwcls.ObjKey{Name: skip}
				}
			}
		}
		if !truncated || count >= (maxN+1)/2 || (attempt > listSoftAttempts && count >= 1) {
			break
		}
	}
	res.Truncated = truncated
	if !truncated {
		res.NextMarker = ""
	}
	return res, nil
}
```

`res.NextMarker` is cleared when not truncated so that G's contract field means "where the next page starts"; radosgw omits the element in that case ([rgw_rest_s3.cc:1963-1965](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1963-L1965)), which the handler mirrors. Write `listObjectsUnordered` ([:2181-2330](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2181-L2330)) the same way with `readAhead = maxN + min(maxN, 100)`, the `while truncated && count <= maxN` loop over `listUnordered`, and no prefixes. `objectEntry` builds the `op.ObjectEntry` as stated under Interfaces.

`listOrdered`:

```go
func (s *Store) listOrdered(ctx context.Context, rec *op.BucketRecord, startAfter rgwcls.ObjKey, prefix, delimiter string,
	numEntries uint32, listVersions bool, attempt uint16) ([]rgwcls.DirEntry, bool, bool, rgwcls.ObjKey, error) {
	pool, err := s.indexPool(ctx, &rec.Info)
	if err != nil {
		return nil, false, false, rgwcls.ObjKey{}, err
	}
	oids := shardOIDs(&rec.Info, rec.Info.Layout.Current)
	shardCount := uint32(len(oids)) //nolint:gosec // shard counts are small
	if shardCount == 0 {
		return nil, false, false, rgwcls.ObjKey{}, fmt.Errorf("%w: the bucket index shard count is 0", op.ErrInvalidBucketState)
	}
	perShard := calcPerShard(numEntries, shardCount)
	switch {
	case attempt == 0:
	case attempt <= 11:
		perShard = min(numEntries, (1<<(attempt-1))*perShard)
	default:
		perShard = numEntries
	}
	if perShard == 0 {
		return nil, false, false, rgwcls.ObjKey{}, fmt.Errorf("%w: unable to calculate the number of entries per shard", op.ErrInvalidBucketState)
	}
	indexVer := rgwcls.EntryVer{Pool: pool.ID()}
	results := make([]rgwcls.ListRet, len(oids))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(s.opts.bucketIndexMaxAIO)
	for i, oid := range oids {
		g.Go(func() error {
			rop := radosclient.NewReadOp()
			r := rgwcls.BucketList(rop, rgwcls.ListOp{StartObj: startAfter, NumEntries: perShard, FilterPrefix: prefix, ListVersions: listVersions, Delimiter: delimiter}, s.release)
			if _, err := pool.Read(gctx, oid, rop, radosclient.OpFlagNone); err != nil {
				return fmt.Errorf("listing index shard %s: %w", oid, err)
			}
			ret, err := r.Result()
			results[i] = ret
			return err
		})
	}
	if err := g.Wait(); err != nil {
		return nil, false, false, rgwcls.ObjKey{}, err
	}
	// k-way merge by name (rgw_rados.cc:9721-9896)
	type cursor struct{ shard, pos int }
	filtered := true
	trackers := make([]cursor, len(results))
	sorted := make([][]rgwcls.DirEntry, len(results))
	for i, r := range results {
		filtered = filtered && r.Dir.Filtered  // the reply's cls_filtered; see the emulator and rgwcls.Dir
		names := slices.Sorted(maps.Keys(r.Dir.Entries))
		for _, n := range names {
			sorted[i] = append(sorted[i], r.Dir.Entries[n])
		}
		trackers[i] = cursor{shard: i}
	}
	var out []rgwcls.DirEntry
	var last rgwcls.ObjKey
	updates := map[string][]rgwcls.Suggestion{}
	count := uint32(0)
	for count < numEntries {
		best := -1
		for i, t := range trackers {
			if t.pos < len(sorted[i]) && (best < 0 || sorted[i][t.pos].Key.Name < sorted[best][trackers[best].pos].Key.Name) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		ent := sorted[best][trackers[best].pos]
		keep := true
		if (!ent.Exists && ent.Flags&(rgwcls.FlagDeleteMarker|rgwcls.FlagCommonPrefix) == 0) || len(ent.PendingMap) > 0 {
			var err error
			keep, err = s.checkDiskState(ctx, rec, indexVer, &ent, updates, oids[best])
			if err != nil {
				return nil, false, false, rgwcls.ObjKey{}, err
			}
		}
		last = ent.Key
		if keep {
			out = append(out, ent)
			count++
		}
		// advance every shard positioned at this name (duplicates across shards, :9871-9884)
		stop := false
		for i := range trackers {
			t := &trackers[i]
			if t.pos < len(sorted[i]) && sorted[i][t.pos].Key.Name == ent.Key.Name {
				t.pos++
				if t.pos == len(sorted[i]) && results[i].IsTruncated {
					stop = true
				}
			}
		}
		if stop {
			break
		}
	}
	s.suggest(ctx, pool, updates)
	truncated := false
	for i, t := range trackers {
		if t.pos < len(sorted[i]) || results[i].IsTruncated {
			truncated = true
			break
		}
	}
	return out, truncated, filtered, last, nil
}
```

`cls_filtered` is not transmitted: `rgw_cls_list_ret::decode` sets it to `struct_v >= 3` (v19.2.6 [`cls_rgw_ops.h:436-439`](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L436-L439), [`:453-460`](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_ops.h#L453-L460)), and every Squid and Tentacle OSD encodes version 4, so `filtered` is a constant `true` in `listOrdered` (replace `r.Dir.Filtered` above with `true`); the `!filtered` client-side rollup branches stay only because the C++ keeps them, and a reviewer may delete them. Phase 0's `ListRet{Dir, IsTruncated, Marker}` needs no change.

`checkDiskState(ctx, rec, indexVer, ent, updates, shardOID)`:

```go
func (s *Store) checkDiskState(ctx context.Context, rec *op.BucketRecord, indexVer rgwcls.EntryVer, ent *rgwcls.DirEntry, updates map[string][]rgwcls.Suggestion, shard string) (bool, error) {
	key, ok := meta.ParseIndexKeyName(ent.Key.Name)
	if !ok {
		return true, nil
	}
	key.Instance = ent.Key.Instance
	st, err := s.heads.StatObject(ctx, rec, key)
	if err != nil {
		if errors.Is(err, op.ErrNotImplemented) {
			return true, nil // no object reads yet: keep the entry, suggest nothing
		}
		return false, err
	}
	ent.PendingMap = nil
	logFlag := s.zone.Zone.LogData
	if !st.Exists && ent.Flags&rgwcls.FlagDeleteMarker == 0 {
		ent.Ver = indexVer
		updates[shard] = append(updates[shard], rgwcls.Suggestion{Op: rgwcls.SuggestRemove, Log: logFlag, Entry: *ent})
		return false, nil
	}
	ent.Meta.Size, ent.Meta.AccountedSize = st.Size, st.Size
	if st.Compression != nil && st.Compression.Type != "" && st.Compression.Type != "none" {
		ent.Meta.AccountedSize = st.Compression.OrigSize
	}
	ent.Meta.Mtime, ent.Meta.ETag, ent.Meta.ContentType, ent.Meta.StorageClass = st.Mtime, st.ETag, st.ContentType, st.StorageClass
	ent.Meta.Category = rgwcls.CategoryMain
	if b, ok := st.Attrs[meta.AttrACL]; ok {
		if pol := acl.DecodePolicy(denc.NewDecoder(b)); pol.Owner.ID != "" {
			ent.Meta.Owner, ent.Meta.OwnerDisplayName = pol.Owner.ID, pol.Owner.DisplayName
		}
	}
	_, ent.Meta.AppendableValue = st.Attrs[meta.AttrPrefix+"append_part_num"]
	// radosgw's delete_obj_index sweep of the multipart parts runs here
	// (rgw_rados.cc:10413-10429 at v19.2.6, :11337-11353 at v20.2.4): every
	// stripe of st.Manifest in the multipart namespace gets a complete_del.
	// It is not implemented yet.
	if pl, err := s.Placement(rec.Info.PlacementRule); err == nil {
		if dp, err := s.pools.get(ctx, pl.DataPool); err == nil {
			ent.Ver = rgwcls.EntryVer{Pool: dp.ID(), Epoch: st.Epoch}
		}
	}
	ent.Tag = st.WriteTag
	ent.Exists = true
	updates[shard] = append(updates[shard], rgwcls.Suggestion{Op: rgwcls.SuggestUpdate, Log: logFlag, Entry: *ent})
	return true, nil
}
```

W Task 6 Step 6 inserts the multipart-part `delete_obj_index` sweep at the comment after the `AppendableValue` line.

`s.heads` is the `headStater`; `Open` sets it to `s` itself (the `Store` implements `op.ObjectStore`, `ErrNotImplemented` until R). `suggest`:

```go
func (s *Store) suggest(ctx context.Context, pool radosclient.Pool, updates map[string][]rgwcls.Suggestion) {
	for oid, changes := range updates {
		if len(changes) == 0 {
			continue
		}
		wop := radosclient.NewWriteOp()
		if s.release >= denc.Tentacle { // v20.2.4 rgw_rados.cc:10822-10823; Squid sends it unguarded (tracker #81000)
			wop.AssertExists()
			rgwcls.GuardBucketResharding(wop, s.release)
		}
		rgwcls.SuggestChanges(wop, changes, s.release)
		if err := s.bgAIO.Acquire(ctx, 1); err != nil {
			return
		}
		go func() {
			defer s.bgAIO.Release(1)
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if _, err := pool.Write(bg, oid, wop, radosclient.OpFlagNone); err != nil {
				slog.DebugContext(bg, "dir suggest failed", slog.String("oid", oid), slog.Any("error", err))
			}
		}()
	}
}
```

`s.bgAIO` is a `semaphore.NewWeighted(rgw_bucket_index_max_aio)` created in `Open`.

`internal/op/listobjects.go`: `Name()` is `list_bucket` for both versions, radosgw's `RGWListBucket::name()` ([`rgw_op.h:970`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L970) at v19.2.6, [`:1042`](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L1042) at v20.2.4), which its v2 handler inherits; the usage log files every request under its op's name ([`rgw_log.cc:245`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L245), [`:556-559`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L556-L559)), so `list_bucket_v2` stays a route name only; `Action()` `policy.S3ListBucket`; `OpMask()` read; `Init` loads the bucket (`r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)`, `op.ErrNoSuchBucket` passes through so `VerifyPermission` can still run Z's evaluator and the 404 is answered afterwards, as radosgw's `bucket_exists` does); `VerifyPermission` → `VerifyBucketPermission(ctx, r, policy.S3ListBucket, acl.PermFor(policy.S3ListBucket))`; `Execute`: `r.BucketRec == nil` → `ErrNoSuchBucket`; Tentacle and `Layout.Current.Layout.Type == meta.IndexIndexless` → `ErrMethodNotAllowed.WithMessage("Indexless buckets cannot be listed")`; `AllowUnordered && Delimiter != ""` → `ErrInvalidArgument`; `Max` from `parseValueAndBound(MaxKeys, 0, rgw_max_listing_results, 1000)` (a port of [rgw_op.h:2633-2660](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.h#L2633-L2660), `ErrInvalidArgument` on a non-numeric); `Result, err = r.Env.Buckets.ListObjects(ctx, r.BucketRec, ListObjectsParams{Prefix, Delimiter, Marker, MaxKeys: Max, AllowUnordered})`; `Complete` → `LogUsage(ctx, r, o.Name())` (Task 7's helper).

`internal/s3/listobjects.go`: `listBucket` and `listBucketV2` read the query per `get_common_params`/`get_params` (`versions` is phase 2: answer `op.ErrNotImplemented` when present), run the op, and render the document through G's `writeChunkedXML` (`ctx, w, r, doc`) in the verified order using ordered structs whose every string field is an `xmltext.Text`, the quoted ETag included (G Task 4: the text escaped as radosgw's `XMLFormatter` escapes it, so a key with a control byte or an apostrophe lists under the name it was stored with) (`EncodingType` first when `url`; `Tenant` when tenanted; `Name`, `Prefix`, `MaxKeys`, `Delimiter`, `IsTruncated` = `max != 0 && truncated`; `CommonPrefixes`; `Contents` with `Key`, `LastModified` (`ISO8601`), `ETag` quoted, `Size`, `StorageClass`, `Owner` (v1 always, v2 with `fetch-owner`), `Type`; v1 `Marker` and `NextMarker`; v2 `ContinuationToken`, `NextContinuationToken`, `KeyCount`, `StartAfter`); keys, prefixes and the delimiter URL-encoded when `encoding-type=url` through G's `op.URLEncode(v, encodeSlash)` — radosgw's `url_encode` over `char_needs_url_encoding`, the one encoder every listing shares — with `encodeSlash` true for `Key`, `NextMarker` and `NextKeyMarker` (`dump_urlsafe`'s default, [rgw_rest.h:712](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.h#L712); [rgw_rest_s3.cc:1833](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1833), [:1940](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1940), [:2000](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2000), [:1964](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1964), [:1810](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1810) at v19.2.6) and false for `Prefix`, `Delimiter` and each `CommonPrefixes` entry ([`:1792`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1792), [`:1881`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1881), [`:1891`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1891), [`:2038`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2038) pass `false`). `writeChunkedXML` frames the response as radosgw's `send_response` does: `end_header(..., CHUNKED_TRANSFER_ENCODING)` sends the headers first, then the declaration and the whole document leave in the one flush `send_response` ends with ([rgw_rest_s3.cc:1897-1968](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1897-L1968) and [:2053-2116](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2053-L2116) at v19.2.6, [:2008-2079](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L2008-L2079) and [:2164-2227](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2164-L2227) at v20.2.4), so the response carries `Transfer-Encoding: chunked` and neither `Content-Length` nor `Accept-Ranges`; an error the op returns is an ordinary error response, as `end_header`'s error branch renders one with a `Content-Length` ([rgw_rest.cc:620-624](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest.cc#L620-L624)). Register both in `bucketHandlers()`.

- [ ] **Step 4: Run the suites; expected PASS; `make check`; commit**

Three commits: `feat(meta): parse index key names`, `feat(driver): list objects with radosgw's shard merge and pending-entry reconciliation`, `feat(s3): ListObjects v1 and v2`. Draft PR `feat: ListObjects (unit M task 8)`.

### Task 9: Stats and quota caches, CheckQuota, AdjustStats, the sync workers

**Files:**
- Create: `internal/driver/stats.go`, `internal/driver/stats_test.go`
- Modify: `internal/op/stats.go` (`StatsStore.AdjustStats` when W has not added it; W-D7's signature), `internal/op/opfakes/`, `internal/memstore/stats.go` (the same effective-quota precedence and appliers; `BucketStats` Main only), `internal/driver/store.go` (the caches and the two workers)

**Interfaces:**
- Consumes: Task 6's `readShardHeaders`, `readIndexStats`, `readOwnerStats`, `syncOwnerStats`, `completeOwnerStatsSync`, `ListUserBuckets`; Task 4's `GetUser`, `readAccount`; Task 5's `GetBucket`; Task 12's `options`; `radosclient.Pool.ListObjects`; `op.Stats`, `op.ErrQuotaExceeded`.
- Produces:

```go
package op

type StatsStore interface {
	BucketStats(ctx context.Context, rec *BucketRecord) (Stats, error)
	UserStats(ctx context.Context, owner meta.Owner) (Stats, error)
	CheckQuota(ctx context.Context, rec *BucketRecord, owner meta.Owner, addBytes, addObjs int64) error
	// AdjustStats is RGWQuotaHandler::update_stats after a write or delete.
	AdjustStats(ctx context.Context, rec *BucketRecord, owner meta.Owner, objs, addBytes, removedBytes int64) error
}
```

```go
package driver

// roundedObjSize is rgw_rounded_objsize (rgw_common.h:1635-1638).
func roundedObjSize(b uint64) uint64 { return (b + 4095) &^ 4095 }

// quotaCache is RGWQuotaCache<T> (rgw_quota.cc:48-223): an LRU of
// rgw_bucket_quota_cache_size entries that expire rgw_bucket_quota_ttl after
// they were set and are refreshed in the background once half the ttl has
// passed. Both radosgw caches use the bucket options (:242, :486).
type quotaEntry struct {
	stats        op.Stats
	expiration   time.Time
	asyncRefresh time.Time // zero while a refresh is in flight (StatsAsyncTestSet)
}
type quotaCache struct{ /* mu, entries map[string]*quotaEntry, lru *list.List, size int, ttl time.Duration, now func() time.Time, refreshes sync.WaitGroup */ }
func newQuotaCache(size int, ttl time.Duration, now func() time.Time) *quotaCache
// get is get_stats (:145-171): fetch runs synchronously on a miss or an
// expired entry (ENOENT from it is zero stats) and asynchronously once the
// refresh time has passed, at most one refresh per key at a time.
func (c *quotaCache) get(ctx context.Context, key string, fetch func(context.Context) (op.Stats, error)) (op.Stats, error)
// adjust is RGWQuotaStatsUpdate (:175-223): every field floored at zero.
func (c *quotaCache) adjust(key string, objs, added, removed int64)
func (c *quotaCache) set(key string, st op.Stats)
func (c *quotaCache) wait() // for the specs: outstanding refreshes

type modifiedBucket struct {
	owner  meta.Owner
	bucket meta.BucketID
}

// statsCaches is RGWQuotaHandlerImpl's two caches plus the owner cache's
// modified-bucket set; Store.quota holds one.
type statsCaches struct {
	bucket, owner *quotaCache
	mu            sync.Mutex
	modified      map[string]modifiedBucket
}

func bucketStatsKey(b meta.BucketID) string // "<tenant>/<name>:<id>"
func ownerStatsKey(o meta.Owner) string     // o.String()

// effectiveQuotas is RGWOp::init_quota over RadosStore::get_quota
// (rgw_op.cc:1411-1449, rgw_sal_rados.cc:1937-1941, svc_quota.cc:9-17).
func (s *Store) effectiveQuotas(ctx context.Context, rec *op.BucketRecord, owner meta.Owner) (bucket, user meta.Quota, err error)

// exceeded is RGWQuotaHandlerImpl::check_quota with the two appliers (:769-906).
func exceeded(q meta.Quota, st op.Stats, addBytes, addObjs int64) bool

func (s *Store) BucketStats(ctx context.Context, rec *op.BucketRecord) (op.Stats, error)
func (s *Store) UserStats(ctx context.Context, owner meta.Owner) (op.Stats, error)
func (s *Store) CheckQuota(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, addBytes, addObjs int64) error
func (s *Store) AdjustStats(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, objs, addBytes, removedBytes int64) error

// The two RGWOwnerStatsCache threads (:349-439).
func (s *Store) runBucketsSync(ctx context.Context) error // every rgw_user_quota_bucket_sync_interval: sync_bucket for each modified bucket
func (s *Store) runOwnerSync(ctx context.Context) error   // at start and every rgw_user_quota_sync_interval: sync_all_owners
func (s *Store) syncOwner(ctx context.Context, owner meta.Owner) error        // (:623-663)
func (s *Store) syncAllStats(ctx context.Context, owner meta.Owner) error     // rgw_sync_all_stats (rgw_user.cc:16-55)
func (s *Store) forEachOwner(ctx context.Context, fn func(meta.Owner) error) error // the users.uid pool without ".buckets" objects, then the account pool's "account." objects, listed through the seam
```

Semantics: `BucketStats` is `load_bucket_stats` ([rgw_op.cc:3003-3016](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3003-L3016)): `readIndexStats` (Main only), zero for an indexless layout. `UserStats` is `load_stats` (`readOwnerStats`). `effectiveQuotas`: base = `s.zone.PeriodConfig.BucketQuota` and `.UserQuota`; the owner's quotas from `GetUser` (`Info.BucketQuota`, `Info.UserQuota`) or `readAccount` (`BucketQuota`, `Quota`); `rec.Info.Quota.Enabled` → bucket = it, else the owner's bucket quota when enabled; the owner's user quota when enabled. `exceeded`: `!Enabled` → false; `MaxObjects >= 0 && NumObjects + addObjs > MaxObjects` → true (objects are checked first, [:894-900](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L894-L900)); size: `CheckOnRaw` compares `Size + addBytes` ([:814-835](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L814-L835)), else `SizeRounded + roundedObjSize(addBytes)` ([:769-791](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L769-L791)), against `MaxSize` when `>= 0`. `CheckQuota`: nothing when neither quota is enabled ([:919-921](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L919-L921)); the bucket cache's fetch is `readShardHeaders` summed over ALL categories ([:249-287](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L249-L287); zero for indexless), the owner cache's is `readOwnerStats`; `ErrQuotaExceeded` wraps which quota (`"bucket"`/`"user"`) for the log. `AdjustStats` adjusts both caches and records the bucket as modified (`data_modified`, [:704-715](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L704-L715); `update_stats`, [:957-960](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L957-L960)). `runBucketsSync` swaps the modified set every interval, and for each `GetBucket` (the current instance) then `syncOwnerStats` (`sync_bucket`, [:578-597](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L578-L597), without `check_bucket_shards`, which is dynamic resharding's). `runOwnerSync` runs `syncAllOwners` immediately and then every `rgw_user_quota_sync_interval` ([:415-433](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L415-L433), the loop body runs before the first wait); `syncOwner`: `readOwnerStats`; skip when `!ownerSyncIdle && lastUpdate.Before(lastSync)`; skip when `lastSync.Add(ownerSyncWait).After(now)`; else `syncAllStats`: `ListUserBuckets` in chunks of `rgw_list_buckets_max_chunk`, per bucket `GetBucket` + `syncOwnerStats` (a missing bucket is skipped, an error returns), then `completeOwnerStatsSync` ([rgw_user.cc:16-55](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_user.cc#L16-L55)). Both workers exist only when `rgw_enable_quota_threads` (:488-495). `forEachOwner` lists the `users.uid` pool through the seam, skipping oids ending in `.buckets` ([svc_user_rados.cc:29](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/services/svc_user_rados.cc#L29), the module's filter) and parsing each as a `meta.UserID`, then the account pool's `account.<id>` objects ([account.cc:641](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/account.cc#L641)).

- [ ] **Step 1: Write the failing specs**

`internal/driver/stats_test.go`, package `driver_test` (store as in Task 7, with an injected clock through `driver.SetClockForTest(s, clock)` in `export_test.go`; `rgw_bucket_quota_ttl=600`):

```go
var _ = Describe("StatsStore", func() {
	// alice with BucketQuota{MaxObjects: 2, MaxSize: -1, Enabled: true} and UserQuota{MaxSize: 8192, MaxObjects: -1, Enabled: true}
	// bucket "plain" created through s.CreateBucket, with two shards' headers seeded:
	//   shard 0 Main {TotalSize: 4000, TotalSizeRounded: 4096, NumEntries: 1}, MultiMeta {TotalSize: 100, NumEntries: 1}
	It("reports the Main category for HEAD and all categories for quota", func(ctx SpecContext) {
		st, err := s.BucketStats(ctx, rec)
		Expect(err).NotTo(HaveOccurred())
		Expect(st).To(Equal(op.Stats{Size: 4000, SizeRounded: 4096, NumObjects: 1}), "rgw_op.cc:3003-3016")
		Expect(s.CheckQuota(ctx, rec, alice, 0, 1)).To(MatchError(op.ErrQuotaExceeded), "1 Main + 1 MultiMeta + 1 > MaxObjects 2 (rgw_quota.cc:278-284 sums every category)")
	})
	It("fetches stats on a cold cache once and serves the cache until the ttl", func(ctx SpecContext) {
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
		before := c.Reads("ceph-objectstore.rgw.buckets.index", "", shard0)
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
		Expect(c.Reads("ceph-objectstore.rgw.buckets.index", "", shard0)).To(Equal(before), "a hit inside the ttl")
		clock.Advance(601 * time.Second)
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
		Expect(c.Reads("ceph-objectstore.rgw.buckets.index", "", shard0)).To(Equal(before + 1), "expired: fetched again")
	})
	It("refreshes in the background past half the ttl", func(ctx SpecContext) {
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed())
		before := c.Reads("ceph-objectstore.rgw.buckets.index", "", shard0)
		clock.Advance(301 * time.Second)
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed(), "served from the cache")
		driver.WaitQuotaRefreshesForTest(s)
		Expect(c.Reads("ceph-objectstore.rgw.buckets.index", "", shard0)).To(Equal(before+1), "rgw_quota.cc:149-156")
	})
	It("compares rounded size by default and raw size when check_on_raw is set", func(ctx SpecContext) {
		rec.Info.Quota = meta.Quota{MaxSize: 8192, MaxObjects: -1, Enabled: true}
		Expect(s.CheckQuota(ctx, rec, alice, 4096, 1)).To(MatchError(op.ErrQuotaExceeded), "4096 rounded + 4096 rounded + 100... the MultiMeta bytes are unrounded: 4096+0+4096 > 8192? equal is fine; use 4097")
		Expect(s.CheckQuota(ctx, rec, alice, 1, 1)).To(Succeed(), "4096 + rounded(1)=4096 is not > 8192")
		Expect(s.CheckQuota(ctx, rec, alice, 4097, 1)).To(MatchError(op.ErrQuotaExceeded), "rounded(4097) = 8192; 4096 + 8192 > 8192")
		rec.Info.Quota.CheckOnRaw = true
		Expect(s.CheckQuota(ctx, rec, alice, 4091, 1)).To(Succeed(), "raw: 4100 + 4091 = 8191")
		Expect(s.CheckQuota(ctx, rec, alice, 4093, 1)).To(MatchError(op.ErrQuotaExceeded), "raw: 4100 + 4093 > 8192")
	})
	It("takes the bucket quota from the bucket, then the owner, then the period config", func(ctx SpecContext) {
		// bucket info quota disabled, alice's BucketQuota enabled MaxObjects 2 → exceeded at +1 with 2 objects;
		// alice's BucketQuota disabled and PeriodConfig.BucketQuota{MaxObjects: 1, Enabled: true} → exceeded at +0 with 2 objects;
		// everything disabled → nil without touching the index (Reads unchanged): rgw_op.cc:1438-1446, rgw_quota.cc:919-921
	})
	It("enforces the user quota against the owner's header stats", func(ctx SpecContext) {
		// cls_user header seeded TotalBytesRounded 8192, alice UserQuota MaxSize 8192: +1 byte → ErrQuotaExceeded; disabled → nil
	})
	It("uses the account's quotas for an account owner", func(ctx SpecContext) {
		// account object with BucketQuota{MaxObjects: 1, Enabled: true}; bucket owned by the account; CheckQuota(+1) → ErrQuotaExceeded (rgw_op.cc:1398-1407)
	})
	It("adjusts both caches, floors at zero and marks the bucket for the owner sync", func(ctx SpecContext) {
		Expect(s.CheckQuota(ctx, rec, alice, 0, 0)).To(Succeed()) // prime both caches
		Expect(s.AdjustStats(ctx, rec, alice, 1, 5000, 0)).To(Succeed())
		st := driver.CachedBucketStatsForTest(s, rec)
		Expect(st).To(Equal(op.Stats{Size: 9100, SizeRounded: 4096 + 8192, NumObjects: 3}))
		Expect(s.AdjustStats(ctx, rec, alice, -10, 0, 1<<40)).To(Succeed())
		Expect(driver.CachedBucketStatsForTest(s, rec)).To(Equal(op.Stats{}), "floored at zero, rgw_quota.cc:192-208")
		Expect(driver.ModifiedBucketsForTest(s)).To(HaveKey(bucketStatsKeyForTest(rec)))
	})
	It("syncs modified buckets into the owner's entry on the interval", func(ctx SpecContext) {
		Expect(s.AdjustStats(ctx, rec, alice, 1, 1, 0)).To(Succeed())
		runCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		go func() { _ = driver.RunBucketsSyncForTest(s, runCtx) }()
		clock.Advance(181 * time.Second)
		Eventually(func() uint64 { st, _, _, _ := driver.ReadOwnerStatsForTest(s, ctx, alice); return st.NumObjects }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(BeEquivalentTo(1), "Main only from the index, not the cache's guess")
	})
	It("runs a full owner sync at start, skipping idle owners and recently synced ones", func(ctx SpecContext) {
		// alice: LastStatsUpdate after LastStatsSync and LastStatsSync + 1 day in the past → synced (entry updated, complete_stats_sync sets LastStatsSync = now)
		// bob: LastStatsUpdate before LastStatsSync → skipped (rgw_quota.cc:636-640)
		// carol: LastStatsSync = now - 1h → skipped (:642-648)
	})
})
```

- [ ] **Step 2: Write `stats.go`**

```go
package driver

func roundedObjSize(b uint64) uint64 { return (b + 4095) &^ 4095 }

func bucketStatsKey(b meta.BucketID) string { return b.Tenant + "/" + b.Name + ":" + b.ID }
func ownerStatsKey(o meta.Owner) string     { return o.String() }

func (s *Store) effectiveQuotas(ctx context.Context, rec *op.BucketRecord, owner meta.Owner) (meta.Quota, meta.Quota, error) {
	bucket, user := s.zone.PeriodConfig.BucketQuota, s.zone.PeriodConfig.UserQuota
	var ownerBucket, ownerUser meta.Quota
	if owner.User != nil {
		u, err := s.GetUser(ctx, *owner.User)
		if err != nil {
			return meta.Quota{}, meta.Quota{}, err
		}
		ownerBucket, ownerUser = u.Info.BucketQuota, u.Info.UserQuota
	} else {
		a, err := s.readAccount(ctx, owner.Account)
		if err != nil {
			return meta.Quota{}, meta.Quota{}, err
		}
		ownerBucket, ownerUser = a.BucketQuota, a.Quota
	}
	switch {
	case rec.Info.Quota.Enabled:
		bucket = rec.Info.Quota
	case ownerBucket.Enabled:
		bucket = ownerBucket
	}
	if ownerUser.Enabled {
		user = ownerUser
	}
	return bucket, user, nil
}

func exceeded(q meta.Quota, st op.Stats, addBytes, addObjs int64) bool {
	if !q.Enabled {
		return false
	}
	if q.MaxObjects >= 0 && int64(st.NumObjects)+addObjs > q.MaxObjects { //nolint:gosec // stats fit an int64
		return true
	}
	if q.MaxSize < 0 {
		return false
	}
	if q.CheckOnRaw {
		return int64(st.Size)+addBytes > q.MaxSize //nolint:gosec
	}
	return int64(st.SizeRounded)+int64(roundedObjSize(uint64(max(addBytes, 0)))) > q.MaxSize //nolint:gosec
}

func (s *Store) fetchBucketStatsAll(rec *op.BucketRecord) func(context.Context) (op.Stats, error) {
	return func(ctx context.Context) (op.Stats, error) {
		if rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
			return op.Stats{}, nil
		}
		headers, err := s.readShardHeaders(ctx, &rec.Info)
		if err != nil {
			return op.Stats{}, err
		}
		var st op.Stats
		for _, h := range headers {
			for _, cs := range h.Stats {
				st.Size += cs.TotalSize
				st.SizeRounded += cs.TotalSizeRounded
				st.NumObjects += cs.NumEntries
			}
		}
		return st, nil
	}
}

func (s *Store) BucketStats(ctx context.Context, rec *op.BucketRecord) (op.Stats, error) {
	if rec.Info.Layout.Current.Layout.Type == meta.IndexIndexless {
		return op.Stats{}, nil
	}
	ent, err := s.readIndexStats(ctx, &rec.Info)
	if err != nil {
		return op.Stats{}, op.FromRADOS(err, op.ScopeBucket)
	}
	return op.Stats{Size: ent.Size, SizeRounded: ent.SizeRounded, NumObjects: ent.Count}, nil
}

func (s *Store) UserStats(ctx context.Context, owner meta.Owner) (op.Stats, error) {
	st, _, _, err := s.readOwnerStats(ctx, owner)
	return st, op.FromRADOS(err, op.ScopeUser)
}

// CheckQuota is RGWQuotaHandlerImpl::check_quota (rgw_quota.cc:912-955).
func (s *Store) CheckQuota(ctx context.Context, rec *op.BucketRecord, owner meta.Owner, addBytes, addObjs int64) error {
	bq, uq, err := s.effectiveQuotas(ctx, rec, owner)
	if err != nil {
		return err
	}
	if !bq.Enabled && !uq.Enabled {
		return nil
	}
	if bq.Enabled {
		st, err := s.quota.bucket.get(ctx, bucketStatsKey(rec.Info.Bucket), s.fetchBucketStatsAll(rec))
		if err != nil {
			return op.FromRADOS(err, op.ScopeBucket)
		}
		if exceeded(bq, st, addBytes, addObjs) {
			return fmt.Errorf("bucket quota: %w", op.ErrQuotaExceeded)
		}
	}
	if uq.Enabled {
		st, err := s.quota.owner.get(ctx, ownerStatsKey(owner), func(ctx context.Context) (op.Stats, error) {
			st, _, _, err := s.readOwnerStats(ctx, owner)
			return st, err
		})
		if err != nil {
			return op.FromRADOS(err, op.ScopeUser)
		}
		if exceeded(uq, st, addBytes, addObjs) {
			return fmt.Errorf("user quota: %w", op.ErrQuotaExceeded)
		}
	}
	return nil
}

// AdjustStats is RGWQuotaHandlerImpl::update_stats (:957-960) and data_modified (:704-715).
func (s *Store) AdjustStats(_ context.Context, rec *op.BucketRecord, owner meta.Owner, objs, addBytes, removedBytes int64) error {
	s.quota.bucket.adjust(bucketStatsKey(rec.Info.Bucket), objs, addBytes, removedBytes)
	s.quota.owner.adjust(ownerStatsKey(owner), objs, addBytes, removedBytes)
	s.quota.mu.Lock()
	s.quota.modified[bucketStatsKey(rec.Info.Bucket)] = modifiedBucket{owner: owner, bucket: rec.Info.Bucket}
	s.quota.mu.Unlock()
	return nil
}
```

`quotaCache.get` locks, looks the key up; a hit whose `asyncRefresh` has passed (and is not zero) zeroes `asyncRefresh` and starts `go func() { st, err := fetch(bg); if err == nil { c.set(key, st) } else { c.restoreRefresh(key) } }()` under `c.refreshes`; a hit before `expiration` returns its stats; otherwise `fetch(ctx)` synchronously (`radosclient.ErrNotFound` → zero stats) and `set`. `set` writes `expiration = now + ttl`, `asyncRefresh = now + ttl/2` ([:133-142](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L133-L142)) and evicts the LRU front past `size`. `adjust` applies `RGWQuotaStatsUpdate::update` with the three floors. `runBucketsSync`, `runOwnerSync`, `syncOwner`, `syncAllStats` and `forEachOwner` follow the semantics above; the tickers come from `s.newTicker` (Task 10) so the specs drive them.

- [ ] **Step 3: memstore parity, fakes, run, commit**

`memstore.CheckQuota` adopts `effectiveQuotas`' precedence (bucket info, owner, `Config.Period.PeriodConfig`) and `exceeded`'s appliers; `memstore.AdjustStats` adjusts its per-bucket and per-owner totals. `make generate-check && make check`. `feat(driver): quota caches, enforcement and the owner stats sync workers`. Draft PR `feat(driver): stats and quota (unit M task 9)`.

### Task 10: The usage-log accumulator and flush worker

**Files:**
- Create: `internal/driver/usage.go`, `internal/driver/usage_test.go`
- Modify: `internal/op/usage.go` (`UsageEntry.Payer meta.Owner`, additive), `internal/op/opfakes/`, `internal/memstore/usage.go` (stores the payer), `internal/driver/store.go` (`AddWorker("usage-flush", ...)`), `internal/testutil/fakerados/cls_rgw.go` (`user_usage_log_add`: appends the decoded entries to a per-object list exposed as `Cluster.UsageEntries(pool, ns, oid) []rgwcls.UsageLogEntry`)

**Interfaces:**
- Consumes: `cls/rgw` (`UsageLogAdd`, `UsageLogInfo`, `UsageLogEntry`, `UsageData`), `meta.StrHashLinux`, Task 12's `options` and `shardMod`, `op.UsageEntry`.
- Produces:

```go
package op

type UsageEntry struct {
	Owner         meta.Owner // the bucket owner (or the requester without a bucket)
	Payer         meta.Owner // the requester when the bucket is requester-pays and the status is not 403; zero otherwise
	Bucket        string
	Time          time.Time
	Category      string
	BytesSent     uint64
	BytesReceived uint64
	Ops           uint64
	SuccessfulOps uint64
}
```

```go
package driver

const usageObjPrefix = "usage." // RGW_USAGE_OBJ_PREFIX, rgw_rados.cc:120

// usageKey is rgw_user_bucket; usageBatch is RGWUsageBatch: one
// rgw_usage_log_entry per hour epoch.
type usageKey struct{ user, bucket string }
type usageBatch map[uint64]*rgwcls.UsageLogEntry

// usageLogger is rgw_log.cc's UsageLogger (:95-180).
type usageLogger struct{ /* mu, entries map[usageKey]usageBatch, numEntries int, roundTS time.Time, opts, now, flush func(ctx, map) error */ }

// roundToHour is utime_t::round_to_hour.
func roundToHour(t time.Time) time.Time

// usageOID is usage_log_hash (rgw_rados.cc:1615-1628).
func (s *Store) usageOID(user string, index uint32) string

// Log is op.UsageLogger: UsageLogger::insert (:159-165) and insert_user (:139-157).
func (s *Store) Log(ctx context.Context, e op.UsageEntry)

// flushUsage is UsageLogger::flush and RGWRados::log_usage (:167-175; rgw_rados.cc:1630-1672).
func (s *Store) flushUsage(ctx context.Context) error

// runUsageFlush flushes every rgw_usage_log_tick_interval and once more when ctx ends.
func (s *Store) runUsageFlush(ctx context.Context) error
```

Semantics: `Log` is a no-op when `rgw_enable_usage_log` is false ([rgw_log.cc:558](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L558) gates on it) or the entry has no owner ([rgw_rados.cc:1645-1648](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1645-L1648) would skip it at flush; skipping early is equivalent); the key's user is the payer when set else the owner ([:159-165](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L159-L165)); when `e.Time` is more than an hour past the current round timestamp the round timestamp moves to `roundToHour(e.Time)` ([:141-142](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L141-L142)); the entry's `Epoch` is the round timestamp in seconds ([:143](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L143)); the batch for the key aggregates into the epoch's entry (`add_usage(category, data)`: `UsageMap[category]` and `TotalUsage` both summed, [cls_rgw_types.h:1035-1038](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L1035-L1038)), and a new (key, epoch) counts toward `numEntries` ([:148-150](https://github.com/ceph/ceph/blob/v19.2.6/src/cls/rgw/cls_rgw_types.h#L148-L150)); past `rgw_usage_log_flush_threshold` the caller's goroutine flushes ([:151-156](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L151-L156)). `flushUsage` swaps the map out under the lock and writes it ([:167-175](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L167-L175)): walking the keys in sorted order, one `usageOID(user, index)` per distinct user with `index` incrementing per user change ([:1650-1656](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1650-L1656)), each object's `UsageLogInfo` holding every entry of every batch under it ([:1657-1661](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1657-L1661)), one `user_usage_log_add` write per object in the zone's usage log pool ([:1666-1670](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1666-L1670)); the first error is returned and logged, and the swapped-out batch is dropped either way, as radosgw's `old_map` is. `usageOID`: `val = index; if user != "": val = shardMod(val, usageMaxUserShards) + StrHashLinux(user); return "usage." + shardMod(val, usageMaxShards)` ([:1617-1627](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1617-L1627)). `runUsageFlush` ticks every `rgw_usage_log_tick_interval`, flushes, and flushes once more on shutdown (`~UsageLogger`, [:128-133](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L128-L133)).

- [ ] **Step 1: Write the failing specs**

`internal/driver/usage_test.go`, package `driver_test` (store with `rgw_enable_usage_log=true`, `rgw_usage_max_shards=4`, `rgw_usage_max_user_shards=1`, `rgw_usage_log_flush_threshold=2` and the rgw emulator):

```go
var _ = Describe("usage log", func() {
	t0 := time.Date(2026, 9, 28, 12, 34, 56, 0, time.UTC)
	alice := meta.UserOwner(meta.UserID{ID: "alice"})
	It("names the usage object as usage_log_hash does", func() {
		Expect(driver.UsageOIDForTest(s, "alice", 0)).To(Equal("usage." + fmt.Sprint(meta.StrHashLinux("alice")%4)))
		Expect(driver.UsageOIDForTest(s, "", 5)).To(Equal("usage.1"), "no user: index % max_shards")
	})
	It("aggregates entries of one hour, one owner and one bucket, and flushes them to the usage pool", func(ctx SpecContext) {
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0, Category: "put_obj", BytesReceived: 10, Ops: 1, SuccessfulOps: 1})
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0.Add(time.Minute), Category: "get_obj", BytesSent: 5, Ops: 1, SuccessfulOps: 0})
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		oid := driver.UsageOIDForTest(s, "alice", 0)
		entries := c.UsageEntries("ceph-objectstore.rgw.log", "usage", oid)
		Expect(entries).To(HaveLen(1), "one entry per (owner, bucket, hour)")
		e := entries[0]
		Expect(e.Owner).To(Equal("alice"))
		Expect(e.Bucket).To(Equal("plain"))
		Expect(e.Epoch).To(BeEquivalentTo(time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC).Unix()), "round_to_hour")
		Expect(e.UsageMap["put_obj"]).To(Equal(rgwcls.UsageData{BytesReceived: 10, Ops: 1, SuccessfulOps: 1}))
		Expect(e.UsageMap["get_obj"]).To(Equal(rgwcls.UsageData{BytesSent: 5, Ops: 1}))
		Expect(e.TotalUsage).To(Equal(rgwcls.UsageData{BytesSent: 5, BytesReceived: 10, Ops: 2, SuccessfulOps: 1}))
	})
	It("starts a new entry for the next hour and keys a requester-pays entry by the payer", func(ctx SpecContext) {
		bob := meta.UserOwner(meta.UserID{ID: "bob"})
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0, Category: "get_obj", Ops: 1})
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "plain", Time: t0.Add(2 * time.Hour), Category: "get_obj", Ops: 1})
		s.Log(ctx, op.UsageEntry{Owner: alice, Payer: bob, Bucket: "plain", Time: t0, Category: "get_obj", Ops: 1})
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		aliceEntries := c.UsageEntries("ceph-objectstore.rgw.log", "usage", driver.UsageOIDForTest(s, "alice", 0))
		Expect(aliceEntries).To(HaveLen(2))
		bobEntries := c.UsageEntries("ceph-objectstore.rgw.log", "usage", driver.UsageOIDForTest(s, "bob", 1))
		Expect(bobEntries).To(HaveLen(1), "rgw_log.cc:159-165 keys by payer; the second distinct user gets index 1")
		Expect(bobEntries[0].Payer).To(Equal("bob"))
		Expect(bobEntries[0].Owner).To(Equal("alice"))
	})
	It("flushes when the threshold is passed and on the tick", func(ctx SpecContext) {
		for i := range 3 {
			s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: fmt.Sprintf("b%d", i), Time: t0, Category: "put_obj", Ops: 1})
		}
		Expect(c.UsageEntries("ceph-objectstore.rgw.log", "usage", driver.UsageOIDForTest(s, "alice", 0))).To(HaveLen(3), "threshold 2 was passed by the third distinct entry")
		s.Log(ctx, op.UsageEntry{Owner: alice, Bucket: "late", Time: t0, Category: "put_obj", Ops: 1})
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() { done <- s.RunUsageFlushForTest(runCtx) }()
		clock.Advance(31 * time.Second) // the injected ticker
		Eventually(func() int { return len(c.UsageEntries("ceph-objectstore.rgw.log", "usage", driver.UsageOIDForTest(s, "alice", 0))) }).WithTimeout(time.Second).WithPolling(time.Millisecond).Should(Equal(4))
		cancel()
		Eventually(done).WithTimeout(time.Second).Should(Receive(Succeed()))
	})
	It("logs nothing when rgw_enable_usage_log is false", func(ctx SpecContext) {
		// a second store opened with rgw_enable_usage_log=false: Log then flush; no usage objects
	})
	It("drops an entry without an owner", func(ctx SpecContext) {
		s.Log(ctx, op.UsageEntry{Bucket: "plain", Time: t0, Category: "get_obj", Ops: 1})
		Expect(s.FlushUsageForTest(ctx)).To(Succeed())
		Expect(c.Objects("ceph-objectstore.rgw.log", "usage")).To(BeEmpty(), "rgw_rados.cc:1645-1648")
	})
})
```

`export_test.go` exposes `UsageOIDForTest`, `FlushUsageForTest`, `RunUsageFlushForTest` and lets the spec inject a fake clock (`clock` with `Now()` and `Advance`, and a `Ticker` the worker uses); the worker takes its ticker from `s.newTicker`, defaulting to `time.NewTicker`.

- [ ] **Step 2: Write `usage.go`**

```go
package driver

type usageKey struct{ user, bucket string }
type usageBatch map[uint64]*rgwcls.UsageLogEntry

type usageLogger struct {
	mu         sync.Mutex
	entries    map[usageKey]usageBatch
	numEntries int
	roundTS    time.Time
}

func roundToHour(t time.Time) time.Time { return t.UTC().Truncate(time.Hour) }

func (s *Store) usageOID(user string, index uint32) string {
	val := index
	if user != "" {
		val = shardMod(val, s.opts.usageMaxUserShards) + meta.StrHashLinux(user)
	}
	return usageObjPrefix + strconv.FormatUint(uint64(shardMod(val, s.opts.usageMaxShards)), 10)
}

// Log is UsageLogger::insert (rgw_log.cc:159-165) and insert_user (:139-157).
func (s *Store) Log(ctx context.Context, e op.UsageEntry) {
	if !s.opts.usageLogEnabled || e.Owner.String() == "" {
		return
	}
	user := e.Owner.String()
	if e.Payer.String() != "" {
		user = e.Payer.String()
	}
	u := &s.usage
	u.mu.Lock()
	if e.Time.After(u.roundTS.Add(time.Hour)) {
		u.roundTS = roundToHour(e.Time)
	}
	epoch := uint64(u.roundTS.Unix()) //nolint:gosec // post-1970
	key := usageKey{user: user, bucket: e.Bucket}
	batch, ok := u.entries[key]
	if !ok {
		batch = usageBatch{}
		u.entries[key] = batch
	}
	ent, ok := batch[epoch]
	if !ok {
		ent = &rgwcls.UsageLogEntry{Owner: e.Owner.String(), Payer: e.Payer.String(), Bucket: e.Bucket, Epoch: epoch, UsageMap: map[string]rgwcls.UsageData{}}
		batch[epoch] = ent
		u.numEntries++
	}
	data := rgwcls.UsageData{BytesSent: e.BytesSent, BytesReceived: e.BytesReceived, Ops: e.Ops, SuccessfulOps: e.SuccessfulOps}
	ent.UsageMap[e.Category] = addUsage(ent.UsageMap[e.Category], data)
	ent.TotalUsage = addUsage(ent.TotalUsage, data)
	needFlush := u.numEntries > s.opts.usageFlushThreshold
	u.mu.Unlock()
	if needFlush {
		if err := s.flushUsage(ctx); err != nil {
			slog.ErrorContext(ctx, "usage log flush failed", slog.Any("error", err))
		}
	}
}

func addUsage(a, b rgwcls.UsageData) rgwcls.UsageData {
	return rgwcls.UsageData{BytesSent: a.BytesSent + b.BytesSent, BytesReceived: a.BytesReceived + b.BytesReceived, Ops: a.Ops + b.Ops, SuccessfulOps: a.SuccessfulOps + b.SuccessfulOps}
}

// flushUsage is UsageLogger::flush (:167-175) and RGWRados::log_usage (rgw_rados.cc:1630-1672).
func (s *Store) flushUsage(ctx context.Context) error {
	u := &s.usage
	u.mu.Lock()
	old := u.entries
	u.entries = map[usageKey]usageBatch{}
	u.numEntries = 0
	u.mu.Unlock()
	if len(old) == 0 {
		return nil
	}
	keys := slices.SortedFunc(maps.Keys(old), func(a, b usageKey) int {
		return cmp.Or(cmp.Compare(a.user, b.user), cmp.Compare(a.bucket, b.bucket))
	})
	objs := map[string]*rgwcls.UsageLogInfo{}
	var index uint32
	lastUser, oid := "", ""
	for _, k := range keys {
		if k.user != lastUser {
			oid = s.usageOID(k.user, index)
			index++
		}
		lastUser = k.user
		info := objs[oid]
		if info == nil {
			info = &rgwcls.UsageLogInfo{}
			objs[oid] = info
		}
		for _, epoch := range slices.Sorted(maps.Keys(old[k])) {
			info.Entries = append(info.Entries, *old[k][epoch])
		}
	}
	pool, err := s.pools.get(ctx, s.zone.Params.UsageLogPool)
	if err != nil {
		return err
	}
	for _, oid := range slices.Sorted(maps.Keys(objs)) {
		wop := radosclient.NewWriteOp()
		rgwcls.UsageLogAdd(wop, *objs[oid], s.release)
		if _, err := pool.Write(ctx, oid, wop, radosclient.OpFlagNone); err != nil {
			return fmt.Errorf("writing usage log %s: %w", oid, err)
		}
	}
	return nil
}

func (s *Store) runUsageFlush(ctx context.Context) error {
	t := s.newTicker(s.opts.usageTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer cancel()
			if err := s.flushUsage(bg); err != nil {
				slog.Error("final usage log flush failed", slog.Any("error", err))
			}
			return nil
		case <-t.C():
			if err := s.flushUsage(ctx); err != nil {
				slog.ErrorContext(ctx, "usage log flush failed", slog.Any("error", err))
			}
		}
	}
}
```

`s.newTicker` returns a small `ticker` interface (`C() <-chan time.Time`, `Stop()`) wrapping `time.Ticker`; the spec's fake clock provides the other implementation. `Open` initialises `s.usage.entries`, `s.usage.roundTS = roundToHour(now)` and registers `AddWorker("usage-flush", s.runUsageFlush)` only when `usageLogEnabled`.

- [ ] **Step 3: Extend `op.UsageEntry`, memstore and the fakes; run; commit**

`make generate-check && make check`. `feat(driver): accumulate and flush the usage log as radosgw shards it`. Draft PR `feat(driver): usage log (unit M task 10)`.

### Task 11: Bucket ACL, policy and tagging subresources

**Files:**
- Create: `internal/op/bucketsubres.go`, `internal/op/bucketsubres_test.go`, `internal/s3/acl.go`, `internal/s3/acl_test.go`, `internal/s3/bucketsubres.go`, `internal/s3/bucketsubres_test.go`
- Modify: `internal/s3/bucket.go` (register `get_acls`, `put_acls`, `get_bucket_policy`, `put_bucket_policy`, `delete_bucket_policy`, `get_bucket_tags`, `put_bucket_tags`, `delete_bucket_tags`)

**Interfaces:**
- Consumes: Z's `acl.DecodePolicy`, `acl.Policy.MarshalS3XML`, `acl.Policy.Encode`, `acl.DefaultPolicy`, `acl.PermFor`, `acl.DecodePublicAccessBlock`; `tags.Attr`, `tags.MaxBucketTags`, `tags.ParseXML`, `tags.Decode`, `tags.Set.Encode`, `tags.Set.MarshalXML`; `authz.Evaluator.BuildACL`, `ParseBucketPolicy`, `UserResolver`, `ErrorFor`; `policy.Policy.IsPublic`, `policy.SemanticsFor`, `policy.S3GetBucketAcl`, `S3PutBucketAcl`, `S3GetBucketPolicy`, `S3PutBucketPolicy`, `S3DeleteBucketPolicy`, `S3GetBucketTagging`, `S3PutBucketTagging`; `op.BucketStore.PutBucketAttrs`, `op.ErrNoSuchBucketPolicy`, `op.ErrNoSuchTagSet`, `op.ErrMalformedXML`, `op.ErrInvalidRange`, `op.ErrAccessDenied`, `op.ErrConcurrentModification`, `op.ErrNoSuchBucket`; `meta.AttrACL`, `meta.AttrPrefix`; `cephconf.Options` (`rgw_max_put_param_size`).
- Produces:

```go
package op

// AttrIAMPolicy is RGW_ATTR_IAM_POLICY (rgw_common.h:154); AttrPublicAccess
// is RGW_ATTR_PUBLIC_ACCESS.
const (
	AttrIAMPolicy    = meta.AttrPrefix + "iam-policy"
	AttrPublicAccess = meta.AttrPrefix + "public-access"
)

// RetryRacedBucketWrite is retry_raced_bucket_write (rgw_op.h:184-196): f,
// and on ErrConcurrentModification up to 15 re-reads of the bucket into
// r.BucketRec followed by f again.
func RetryRacedBucketWrite(ctx context.Context, r *Request, f func() error) error

// BucketACLFor is rgw_op_get_bucket_policy_from_attr (rgw_op.cc:267-286):
// the stored ACL, or a default owner-only policy with an empty display name.
func BucketACLFor(rec *BucketRecord) (acl.Policy, error)

type GetBucketACL struct{ XML []byte }                       // RGWGetACLs at bucket scope (rgw_op.cc:5695-5734)
type PutBucketACL struct{ ACL acl.Policy }                    // RGWPutACLs at bucket scope (:5821-5928); the handler builds ACL through authz.BuildACL
type GetBucketPolicy struct{ JSON []byte }                    // RGWGetBucketPolicy (:8146-8167)
type PutBucketPolicy struct{ Policy *policy.Policy }          // RGWPutBucketPolicy (:8084-8121); parsed by the handler through authz.ParseBucketPolicy
type DeleteBucketPolicy struct{}                              // (:8195-8210)
type GetBucketTagging struct{ Set tags.Set }                  // RGWGetBucketTags (:1159-1169)
type PutBucketTagging struct{ Set tags.Set }                  // RGWPutBucketTags (:1183-1203); parsed by the handler
type DeleteBucketTagging struct{}                             // RGWDeleteBucketTags (:1223-1240)
```

```go
package s3

// objectGetACLs and objectPutACLs are the object-scope halves of the shared
// get_acls/put_acls routes, which radosgw names once for bucket and object
// scope; object.go assigns them, and while unassigned they answer
// op.ErrNotImplemented.
var objectGetACLs, objectPutACLs HandlerFunc

// writeXMLDeclaration is end_header + dump_start with no document: the
// `<?xml version="1.0" encoding="UTF-8"?>` body radosgw sends on a 200 PUT.
func writeXMLDeclaration(w http.ResponseWriter, r *op.Request, status int)
```

Semantics, each from the C++ cited: every op's `Init` loads the bucket and `VerifyPermission` is `VerifyBucketPermission` with its action and `acl.PermFor(action)`; `GetBucketACL` answers `ErrNoSuchBucket` before the permission check when the bucket is missing (:5707-5709) and renders `BucketACLFor(rec).MarshalS3XML()`. `PutBucketACL.Execute`: the ACL's owner must equal the stored ACL's owner, else `ErrAccessDenied.WithMessage("Cannot modify ACL Owner")` ([:5859-5864](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5859-L5864), radosgw's `-EPERM`); a `PublicAccessBlock` attr with `BlockPublicACLs` and a public ACL → `ErrAccessDenied` ([:5905-5910](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5905-L5910)); `PutBucketAttrs(rec, {AttrACL: encode(ACL)}, nil)`; `ErrConcurrentModification` → success ([:5925-5927](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5925-L5927)). `GetBucketPolicy`: the attr absent or empty → `ErrNoSuchBucketPolicy.WithMessage("The bucket policy does not exist")` ([:8146-8167](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8146-L8167)). `PutBucketPolicy`: a `PublicAccessBlock` with `BlockPublicPolicy` and `Policy.IsPublic(SemanticsFor(release))` → `ErrAccessDenied` ([:8103-8108](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8103-L8108)); `RetryRacedBucketWrite { PutBucketAttrs(rec, {AttrIAMPolicy: []byte(Policy.Text)}, nil) }` ([:8110-8115](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8110-L8115)). `DeleteBucketPolicy`: `RetryRacedBucketWrite { PutBucketAttrs(rec, nil, [AttrIAMPolicy]) }` ([:8204-8209](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L8204-L8209)). `GetBucketTagging`: `tags.Attr` absent → `ErrNoSuchTagSet` ([:1161-1167](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1161-L1167)), else `tags.Decode`. `PutBucketTagging`: `RetryRacedBucketWrite { PutBucketAttrs(rec, {tags.Attr: encode(Set)}, nil) }` ([:1197-1201](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1197-L1201)). `DeleteBucketTagging`: `RetryRacedBucketWrite { PutBucketAttrs(rec, nil, [tags.Attr]) }` ([:1232-1240](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L1232-L1240)). The master forwarding calls are no-ops on the single zone, which is the master.

Handlers (`rgw_rest_s3.cc`): `get_acls` and `put_acls` branch on `r.Scope()`: object scope → `objectGetACLs`/`objectPutACLs`; bucket scope: GET renders the XML as `application/xml` (:3605-3614); PUT reads the body (over `rgw_max_put_param_size` → `ErrMalformedXML.WithMessage("The XML you provided was larger than the maximum <n> bytes allowed.")`, [:5830-5837](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5830-L5837)), refuses a canned-ACL header together with a body with `ErrInvalidArgument` ([:5844-5847](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L5844-L5847)), drops a `bucket-*` canned ACL ([:3640-3643](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L3640-L3643)), builds the policy through `authz.Evaluator.BuildACL(ctx, r, resolver, existing, body, false)` (canned, grant headers, XML, the grant limit and the owner check are Z's), and answers 200 with the XML declaration body. `get_bucket_policy` → 200 `application/json` with the bytes; `put_bucket_policy` reads the body (over the limit → `ErrInvalidRange`, radosgw's `-ERANGE` → 416), parses through `authz.ParseBucketPolicy` (its errors through `authz.ErrorFor`), answers 204; `delete_bucket_policy` → 204. `get_bucket_tags` → 200 with `Set.MarshalXML()` (`<Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet>…</TagSet></Tagging>`); `put_bucket_tags` reads the body (limit → `ErrInvalidRange`), `tags.ParseXML(body, tags.MaxBucketTags)` (`ErrMalformedXML`/`ErrInvalidTag` through `authz.ErrorFor`), answers 200 with the XML declaration; `delete_bucket_tags` → 204.

- [ ] **Step 1: Write the failing op specs**

`internal/op/bucketsubres_test.go` on `memstore` with `alice` owning `plain` (its `user.rgw.acl` from `acl.DefaultPolicy`), `Authz: op.OwnerOnly{}`:

```go
var _ = Describe("bucket subresource ops", func() {
	It("renders the stored ACL, and a default one for a bucket written without an ACL attr", func(ctx SpecContext) {
		o := &op.GetBucketACL{}
		Expect(op.Run(ctx, o, req("GET", "plain"))).To(Succeed())
		Expect(string(o.XML)).To(ContainSubstring(`<Owner><ID>alice</ID>`))
		Expect(store.PutBucketAttrs(ctx, rec, nil, []string{meta.AttrACL})).To(Succeed())
		o = &op.GetBucketACL{}
		Expect(op.Run(ctx, o, req("GET", "plain"))).To(Succeed())
		Expect(string(o.XML)).To(ContainSubstring(`<Owner><ID>alice</ID></Owner>`), "create_default(owner, \"\"), rgw_op.cc:283: no DisplayName")
	})
	It("is NoSuchBucket before any permission check", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetBucketACL{}, req("GET", "missing"))).To(MatchError(op.ErrNoSuchBucket))
	})
	It("refuses to change the ACL owner and tolerates a lost race", func(ctx SpecContext) {
		p := acl.DefaultPolicy(meta.UserOwner(meta.UserID{ID: "bob"}), "Bob")
		err := op.Run(ctx, &op.PutBucketACL{ACL: p}, req("PUT", "plain"))
		Expect(err).To(MatchError(op.ErrAccessDenied))
		Expect(op.AsError(err).Message).To(Equal("Cannot modify ACL Owner"))
		// memstore fake: a stale rec on the request → ErrConcurrentModification from PutBucketAttrs → success
	})
	It("stores, returns and deletes a bucket policy with radosgw's errors", func(ctx SpecContext) {
		g := &op.GetBucketPolicy{}
		err := op.Run(ctx, g, req("GET", "plain"))
		Expect(err).To(MatchError(op.ErrNoSuchBucketPolicy))
		Expect(op.AsError(err).Message).To(Equal("The bucket policy does not exist"))
		pol, perr := policy.Parse(allowAll, policy.ParseOptions{Release: denc.Squid})
		Expect(perr).NotTo(HaveOccurred())
		Expect(op.Run(ctx, &op.PutBucketPolicy{Policy: pol}, req("PUT", "plain"))).To(Succeed())
		g = &op.GetBucketPolicy{}
		Expect(op.Run(ctx, g, req("GET", "plain"))).To(Succeed())
		Expect(string(g.JSON)).To(Equal(allowAll), "the text verbatim, rgw_op.cc:8111-8112")
		Expect(op.Run(ctx, &op.DeleteBucketPolicy{}, req("DELETE", "plain"))).To(Succeed())
		Expect(op.Run(ctx, &op.GetBucketPolicy{}, req("GET", "plain"))).To(MatchError(op.ErrNoSuchBucketPolicy))
	})
	It("blocks a public policy when the bucket blocks public policies", func(ctx SpecContext) {
		// PublicAccessBlock{BlockPublicPolicy: true} encoded under op.AttrPublicAccess on the bucket; allowAll → ErrAccessDenied (rgw_op.cc:8103-8108)
	})
	It("stores, returns and deletes tags with NoSuchTagSet when absent", func(ctx SpecContext) {
		Expect(op.Run(ctx, &op.GetBucketTagging{}, req("GET", "plain"))).To(MatchError(op.ErrNoSuchTagSet))
		var set tags.Set
		Expect(set.Add("k", "v", tags.MaxBucketTags)).To(Succeed())
		Expect(op.Run(ctx, &op.PutBucketTagging{Set: set}, req("PUT", "plain"))).To(Succeed())
		g := &op.GetBucketTagging{}
		Expect(op.Run(ctx, g, req("GET", "plain"))).To(Succeed())
		Expect(g.Set.Tags).To(Equal([]tags.Tag{{Key: "k", Value: "v"}}))
		Expect(op.Run(ctx, &op.DeleteBucketTagging{}, req("DELETE", "plain"))).To(Succeed())
		Expect(op.Run(ctx, &op.GetBucketTagging{}, req("GET", "plain"))).To(MatchError(op.ErrNoSuchTagSet))
	})
	It("retries a raced write fifteen times then gives up", func(ctx SpecContext) {
		// opfakes.FakeBucketStore whose PutBucketAttrs returns ErrConcurrentModification always and GetBucket returns rec:
		// RetryRacedBucketWrite → ErrConcurrentModification; PutBucketAttrsCallCount == 16; GetBucketCallCount == 15 (rgw_op.h:189)
	})
})
```

- [ ] **Step 2: Write the failing handler specs**

`internal/s3/acl_test.go` and `bucketsubres_test.go` on `memstore` through `newHandler` with an authenticator for `alice`: `GET /plain?acl` → 200 `application/xml` starting with `<?xml version="1.0" encoding="UTF-8"?><AccessControlPolicy xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`; `PUT /plain?acl` with `x-amz-acl: public-read` → 200 and a body of exactly `<?xml version="1.0" encoding="UTF-8"?>`, then `GET /plain?acl` shows the AllUsers READ grant; `PUT /plain?acl` with a 2 MiB body → 400 `MalformedXML` with radosgw's message; `x-amz-acl` together with a body → 400 `InvalidArgument`; `GET /plain/k?acl` → 501 until R assigns `objectGetACLs` (the M-D8 seam); `GET /plain?policy` → 404 `NoSuchBucketPolicy` with `<Message>The bucket policy does not exist</Message>`; `PUT /plain?policy` with `allowAll` → 204; `GET /plain?policy` → 200 `application/json` body `allowAll`; a malformed policy → 400 with Z's message through `authz.ErrorFor`; `DELETE /plain?policy` → 204; `GET /plain?tagging` → 404 `NoSuchTagSet`; `PUT /plain?tagging` with `<Tagging><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>` → 200 with the declaration body; `GET /plain?tagging` → 200 `<?xml version="1.0" encoding="UTF-8"?><Tagging xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><TagSet><Tag><Key>k</Key><Value>v</Value></Tag></TagSet></Tagging>`; 51 tags → 400 `InvalidTag`; `DELETE /plain?tagging` → 204.

- [ ] **Step 3: Write the ops and handlers**

```go
package op

func RetryRacedBucketWrite(ctx context.Context, r *Request, f func() error) error {
	err := f()
	for i := 0; i < 15 && errors.Is(err, ErrConcurrentModification); i++ {
		rec, rerr := r.Env.Buckets.GetBucket(ctx, r.Tenant, r.Bucket)
		if rerr != nil {
			return rerr
		}
		r.BucketRec = rec
		err = f()
	}
	return err
}

func BucketACLFor(rec *BucketRecord) (acl.Policy, error) {
	b, ok := rec.Attrs[meta.AttrACL]
	if !ok {
		return acl.DefaultPolicy(rec.Info.Owner, ""), nil
	}
	d := denc.NewDecoder(b)
	p := acl.DecodePolicy(d)
	if err := d.Err(); err != nil {
		return acl.Policy{}, fmt.Errorf("%w: decoding bucket acl: %w", ErrInternalError, err)
	}
	return p, nil
}
```

The eight ops follow the semantics above; each `Complete` calls `LogUsage`. `internal/s3/acl.go`:

```go
package s3

var objectGetACLs HandlerFunc = notImplemented
var objectPutACLs HandlerFunc = notImplemented

func getACLs(ctx context.Context, w http.ResponseWriter, r *op.Request) error {
	if r.Scope() == op.ScopeObject {
		return objectGetACLs(ctx, w, r)
	}
	o := &op.GetBucketACL{}
	if err := op.Run(ctx, o, r); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(xml.Header))
	_, err := w.Write(o.XML)
	return err
}
```

(`xml.Header` is `<?xml version="1.0" encoding="UTF-8"?>` followed by a newline; radosgw emits it without the newline — use the constant G's `WriteXML` uses. `notImplemented` is a two-line `HandlerFunc` in `acl.go` returning `op.ErrNotImplemented`.) `putACLs` reads the body under the limit, applies the header/body rule, calls `authz` through `r.Env.Authz.(*authz.Evaluator)` (an `OwnerOnly` stub before Z lands answers `op.ErrNotImplemented` for anything but a canned ACL through `acl.Canned`) and runs `op.PutBucketACL`. `bucketsubres.go` holds the policy and tagging handlers. `bucket.go` registers the eight routes.

- [ ] **Step 4: Run, gate, commit**

`make check`. Two commits: `feat(op): bucket ACL, policy and tagging ops`, `feat(s3): bucket ACL, policy and tagging handlers`. Draft PR `feat: bucket subresources (unit M task 11)`.

### Task 12: Options, shard-count floors and startup validation

**Files:**
- Create: `internal/driver/options.go`, `internal/driver/options_test.go`, `internal/driver/shards.go`, `internal/driver/shards_test.go`
- Modify: `internal/driver/store.go` (`Open` calls `readOptions` once and keeps the result as `s.opts`; the inline reads Tasks 3, 6, 7 and 8 made move here)

**Interfaces:**
- Consumes: `cephconf.Options` (`Bool`, `Int64`, `Uint64`, `Seconds`, `Size`, `String`), `meta.Quota`.
- Produces:

```go
package driver

// options are the rgw_* tunables the metadata plane reads, once, at Open;
// the defaults in the comments are rgw.yaml.in's at v19.2.6 (the same at
// v20.2.4 except rgw_max_listing_results, 1000 → 5000, which the cluster's
// own config supplies).
type options struct {
	cacheEnabled     bool          // rgw_cache_enabled, true
	cacheLRUSize     int           // rgw_cache_lru_size, 25000
	cacheExpiry      time.Duration // rgw_cache_expiry_interval, 900 s
	numControlOIDs   int64         // rgw_num_control_oids, 8
	maxNotifyRetries uint64        // rgw_max_notify_retries, 10

	usageLogEnabled     bool          // rgw_enable_usage_log, false
	usageFlushThreshold int           // rgw_usage_log_flush_threshold, 1024
	usageTick           time.Duration // rgw_usage_log_tick_interval, 30 s
	usageMaxShards      uint32        // rgw_usage_max_shards, 32, floored to 1
	usageMaxUserShards  uint32        // rgw_usage_max_user_shards, 1, floored to 1
	lcMaxObjs           uint32        // rgw_lc_max_objs, 32, floored to 1; for lifecycle, not implemented yet

	bucketQuotaTTL          time.Duration // rgw_bucket_quota_ttl, 600 s
	bucketQuotaCacheSize    int           // rgw_bucket_quota_cache_size, 10000
	bucketSyncInterval      time.Duration // rgw_user_quota_bucket_sync_interval, 180 s
	ownerSyncInterval       time.Duration // rgw_user_quota_sync_interval, 86400 s
	ownerSyncWait           time.Duration // rgw_user_quota_sync_wait_time, 86400 s
	ownerSyncIdle           bool          // rgw_user_quota_sync_idle_users, false
	quotaThreads            bool          // rgw_enable_quota_threads, true
	defaultBucketQuota      meta.Quota    // rgw_bucket_default_quota_max_{objects,size}, -1: Enabled when either is >= 0
	defaultUserQuota        meta.Quota    // rgw_user_default_quota_max_{objects,size}
	defaultAccountQuota     meta.Quota    // rgw_account_default_quota_max_{objects,size}

	listMinReadahead       int    // rgw_list_bucket_min_readahead, 1000
	maxListingResults      int64  // rgw_max_listing_results, 1000 (Squid) / 5000 (Tentacle)
	overrideIndexMaxShards uint32 // rgw_override_bucket_index_max_shards, 0
	bucketIndexMaxAIO      int    // rgw_bucket_index_max_aio, 128
	listBucketsMaxChunk    int    // rgw_list_buckets_max_chunk, 1000
	maxPutParamSize        int64  // rgw_max_put_param_size, 1 MiB
	dynamicResharding      bool   // rgw_dynamic_resharding, true: warned about at startup, never run (docs/exclusions.md)
	runSyncThread          bool   // rgw_run_sync_thread, true: honoured by doing nothing, logged
}

func readOptions(conf *cephconf.Options) (options, error)

// floorShards returns n, or 1 with an error-level log line naming the option
// when n <= 0: radosgw accepts a zero and faults on the first request that
// shards (docs/ceph-upstream-bugs.md, tracker #80991).
func floorShards(option string, n int64) uint32

// shardMod is the plain modulo the usage log uses (usage_log_hash,
// rgw_rados.cc:1615-1628 at v19.2.6: val % max_user_shards, then % max_shards)
// and the driver's one plain modulo of a hash by a shard count. It is NOT
// rgw_shards_mod: the bucket index and GC reduce through the primes
// 7877/65521, which meta.IndexShard does for the index and meta.HashMod
// does for GC (rgw_gc.cc:63-66). A zero count cannot reach it after
// floorShards; it still answers 0 rather than dividing.
func shardMod(hash uint32, shards uint32) uint32
```

- [ ] **Step 1: Write the failing specs**

`internal/driver/options_test.go`, package `driver` (`conf` is Task 1's helper, moved to a shared test file):

```go
var _ = Describe("readOptions", func() {
	It("reads every option with radosgw's defaults", func() {
		o, err := readOptions(conf(nil))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.cacheLRUSize).To(Equal(25000))
		Expect(o.cacheExpiry).To(Equal(900 * time.Second))
		Expect(o.numControlOIDs).To(BeEquivalentTo(8))
		Expect(o.usageMaxShards).To(BeEquivalentTo(32))
		Expect(o.bucketQuotaTTL).To(Equal(10 * time.Minute))
		Expect(o.defaultBucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: -1}), "disabled when both are -1")
		Expect(o.maxPutParamSize).To(BeEquivalentTo(1 << 20))
	})
	It("floors a zero or negative shard count to one and says so", func() {
		var buf bytes.Buffer
		restore := captureLog(&buf) // swaps slog.Default for a JSON handler on buf
		defer restore()
		o, err := readOptions(conf(map[string]string{"rgw_usage_max_shards": "0", "rgw_lc_max_objs": "-4", "rgw_usage_max_user_shards": "0"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.usageMaxShards).To(BeEquivalentTo(1))
		Expect(o.lcMaxObjs).To(BeEquivalentTo(1))
		Expect(o.usageMaxUserShards).To(BeEquivalentTo(1))
		Expect(buf.String()).To(ContainSubstring(`"level":"ERROR"`))
		Expect(buf.String()).To(ContainSubstring("rgw_usage_max_shards"))
		Expect(buf.String()).To(ContainSubstring("80991"))
	})
	It("enables a default quota when one limit is set", func() {
		o, err := readOptions(conf(map[string]string{"rgw_bucket_default_quota_max_objects": "10"}))
		Expect(err).NotTo(HaveOccurred())
		Expect(o.defaultBucketQuota).To(Equal(meta.Quota{MaxSize: -1, MaxObjects: 10, Enabled: true}), "rgw_quota.cc:998-1008")
	})
	It("fails on an option librados does not know", func() {
		_, err := readOptions(cephconf.NewOptions(cephconf.MapGetter{}))
		Expect(err).To(MatchError(cephconf.ErrUnknownOption))
	})
})

var _ = Describe("shardMod", func() {
	It("reduces by the count and never divides by zero", func() {
		Expect(shardMod(17138, 8)).To(BeEquivalentTo(2))
		Expect(shardMod(17138, 0)).To(BeZero())
	})
})
```

- [ ] **Step 2: Write `options.go` and `shards.go`; move the inline reads**

`readOptions` reads each option through the typed accessor its `rgw.yaml.in` type calls for (`Bool` for `bool`, `Int64` for `int`, `Uint64` for `uint`, `Seconds` for `secs`, `Size` for `size`), wrapping the first failure as `fmt.Errorf("reading %s: %w", name, err)`; the quota defaults follow `rgw_apply_default_bucket_quota` ([rgw_quota.cc:998-1008](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_quota.cc#L998-L1008): a `>= 0` objects or size sets the limit and `Enabled`); the three shard counts go through `floorShards`; `Open` logs one warning when `dynamicResharding` is true (`"dynamic resharding is enabled in config but rgw-go runs no reshard worker"`) and one info line when `runSyncThread` is true (`"rgw_run_sync_thread is set; a single zone has nothing to sync"`).

```go
func floorShards(option string, n int64) uint32 {
	if n <= 0 {
		slog.Error("shard count is not positive; using 1 (radosgw would fault on first use, tracker #80991)", slog.String("option", option), slog.Int64("value", n))
		return 1
	}
	return uint32(min(n, math.MaxUint32)) //nolint:gosec // bounded
}

func shardMod(hash uint32, shards uint32) uint32 {
	if shards == 0 {
		return 0
	}
	return hash % shards
}
```

- [ ] **Step 3: Run the suites; expected PASS; `make check`; commit**

`feat(driver): read the metadata-plane options once and floor zero shard counts`. Draft PR `feat(driver): options and shard floors (unit M task 12)`.

### Task 13: driver.Store assembly, memstore parity, the cluster specs and the oracle `[cluster]`

**Files:**
- Modify: `internal/driver/store.go` (the final `Open`: options, pools, cache, notifier, sysobj, quota caches, usage logger, workers; `Close`), `internal/driver/doc.go`, `internal/cli/serve.go` (nothing new: G wires `driver.Open` and `Store.Run`; confirm the `Env` carries every store), `internal/memstore/` (parity fixes found by the shared conformance specs), `docs/exclusions.md` (M-D1 and D4), `docs/ceph-upstream-bugs.md` (the four entries' **rgw-go** lines name their tasks), `hack/rooket/lib.sh` and `hack/rooket/README.md` (an `admin.sh` entry point)
- Create: `internal/op/conformance/conformance.go` (a shared Ginkgo suite body run against `memstore` and, under the integration tag, against the driver), `internal/testutil/cephtest/admin.go` (`RadosgwAdmin`), `hack/rooket/admin.sh`, `test/integration/metadata_test.go`, `test/integration/integration_suite_test.go`

**Interfaces:**
- Consumes: everything above; `cephtest.Conf`, `cephtest.ReadManifest`, `test/gate.Manifest` (`Release`, `RooketName`, `Realm`, `ZoneGroup`, `Zone`, `Pools{Root, Meta, Control, Log, Index, Data, NonEC}`, `Users[]{UID, Tenant, AccessKey, SecretKey}`, `Buckets[]{Name, Owner, ID, Marker, NumShards}`, `Objects[]`), the radosgw endpoint from `hack/rooket/lib.sh`'s `rgw_endpoint` (exposed by a `hack/rooket/endpoint.sh <release>` script beside `admin.sh`), aws-sdk-go-v2 as an S3 client against the coexisting radosgw, `gate`'s JSON diff helper for `radosgw-admin` output. The populate users have no email, so the email spec creates one with `radosgw-admin user modify --email`.
- Produces:

```go
package conformance

// Run declares the store-conformance specs against an op.Env factory: the
// behaviours every implementation of the store interfaces shares (users,
// buckets, listing, stats, usage). memstore's suite and the driver's
// integration suite both call it.
func Run(newEnv func(ctx context.Context) (*op.Env, func()))
```

```go
package cephtest

// RadosgwAdmin runs radosgw-admin in the cluster's toolbox through
// hack/rooket/admin.sh and returns its stdout; the release comes from the
// manifest. It never touches any cluster but the rooket one named by the
// manifest.
func RadosgwAdmin(ctx context.Context, conf string, args ...string) ([]byte, error)
```

- [ ] **Step 1: The final `Open`, in order**

`Open`: release (G); `readOptions` (Task 12); the root pools and `resolveZone` (Task 1); `newPoolCache`; `s.now = time.Now`, `s.newTicker`; the cache, the control pool, `controlOIDs(opts.numControlOIDs)`, `newNotifier`, `createControlObjects`, `AddWorker("control-watch", ...)` (Tasks 2, 3); `sysobjs`; `heads = s`, `bgAIO = semaphore.NewWeighted(int64(opts.bucketIndexMaxAIO))` (Task 8); the quota caches and `AddWorker("quota-buckets-sync", ...)`, `AddWorker("quota-owner-sync", ...)` when `quotaThreads` (Task 9); the usage logger and `AddWorker("usage-flush", ...)` when `usageLogEnabled` (Task 10); the two startup log lines (Task 12). `Close` stops nothing (workers stop with `Run`'s context) and closes the pool cache. `doc.go` states the coexistence obligations the package carries (cache invalidation both ways, the reshard guard on listing suggestions per release, user stats kept exact, no pool or zone creation) with the registry entries by title.

- [ ] **Step 2: Shared conformance specs**

`internal/op/conformance/conformance.go` declares, in one `Describe("store conformance")`, the behaviours the earlier tasks pinned individually, phrased only through the `op` interfaces: a user put then read back with its indexes; `GetUserByEmail` case-insensitive; `PutUser` exclusive twice → `ErrUserAlreadyExists`; `CreateBucket` then `GetBucket` with `Attrs` and both versions; same-owner recreate → `ErrBucketAlreadyExists` with the existing record; `PutBucketInfo` under a stale version → `ErrConcurrentModification`; `ListUserBuckets` pages; `ListObjects` with delimiter, marker and `max-keys=0`; `BucketStats` of an empty bucket is zero; `CheckQuota` with a bucket quota of one object refuses the second; `Log` then `Usage()`/flush shows the entry (memstore exposes `Usage()`, the driver flushes and the spec reads the usage object through the pool). `internal/memstore/conformance_test.go` runs it against `memstore.New`; `test/integration/metadata_test.go` runs it against `driver.Open` on the rooket cluster with a per-spec random bucket-name prefix and a `DeferCleanup` that deletes what it created. Fix `memstore` where the two disagree; the driver's behaviour, being radosgw's, wins.

- [ ] **Step 3: The cluster specs (`test/integration/metadata_test.go`, `//go:build integration`, `Label("integration")`)**

Against `make cluster-up RELEASE=squid && make populate RELEASE=squid` (and again for `tentacle`), with the store opened through `goceph` on `cephtest.Conf()` and `rgw_zone`/`rgw_zonegroup`/`rgw_realm` from the manifest's store name:

1. **Zone resolution against Rook's zoned store.** `s.Zone().Name` and `s.ZoneGroup().Name` are the store; `s.Period().ID` is non-empty; `s.ZoneParams()` marshalled to JSON equals `radosgw-admin zone get` (the gate's JSON diff, ignoring key order); `s.Placement(default rule)` names the shared pools' namespaces the manifest records.
2. **Users the populate script created.** For each manifest user: `GetUser`, `GetUserByAccessKey`, `GetUserByEmail` agree with `radosgw-admin user info --uid`, field by field through the JSON diff of `meta.UserInfo.MarshalJSON` against the admin output.
3. **Buckets radosgw created.** For each manifest bucket: `GetBucket` agrees with `radosgw-admin bucket stats --bucket` on id, marker, owner, placement rule, num_shards and creation time; `ListObjects` of `plain` returns the manifest's keys in order with their sizes, `_underscore.bin` included, and no pending-entry suggestion was needed (the bucket is quiescent).
4. **Cache invalidation, radosgw-admin → rgw-go.** Prime the cache with `GetBucket("plain")`; `radosgw-admin quota set --bucket plain --quota-scope bucket --max-objects 7` then `quota enable`; `Eventually(GetBucket(...).Info.Quota.MaxObjects).WithTimeout(10s)` is 7 without restarting the store — the notify radosgw-admin sent invalidated the entry.
5. **Cache invalidation, rgw-go → radosgw.** Prime the coexisting radosgw's cache by `HeadBucket` through the S3 client; `PutBucketAttrs` on `plain` sets `user.rgw.x-amz-tagging` to a `tags.Set{{"k","v"}}` encoding; `Eventually(aws s3api get-bucket-tagging via the radosgw endpoint)` returns `k=v` — the `UPDATE_OBJ` rgw-go distributed reached the radosgw.
6. **A bucket rgw-go creates is radosgw's bucket.** `CreateBucket("rgwgo-<rand>")` for a manifest user; `radosgw-admin bucket stats --bucket` shows the id and marker `s` chose, `num_shards` 11 and the placement rule; `radosgw-admin bucket list --uid` lists it; `aws s3 ls` through the radosgw lists it and `aws s3api head-bucket` succeeds; `radosgw-admin user stats --uid --sync-stats` runs clean. Then `DeleteBucket`: the admin listing no longer has it and `rados -p <index pool> ls` (toolbox) shows no `.dir.<id>` shard.
7. **Quota and stats.** `radosgw-admin quota set --uid <u> --quota-scope user --max-objects 0` and `quota enable`; `CheckQuota(rec, owner, 1, 1)` → `op.ErrQuotaExceeded`; `radosgw-admin quota disable`; `Eventually(CheckQuota ... succeeds)` within `rgw_bucket_quota_ttl`? No: quotas are re-read from the user object (cache-notified), the STATS are cached; assert the first check after disable passes because the quota, not the stats, changed.
8. **Usage log.** Open a second store with `rgw_enable_usage_log=true` in its config overrides (`cephtest` passes `--rgw-enable-usage-log=true` in the connect args); `Log` one entry for a manifest user and bucket; flush; `radosgw-admin usage show --uid <u> --show-log-entries=true` lists a `put_obj` with the bytes.
9. **The NextMarker quirk.** `aws s3api list-objects --bucket plain --max-keys 1` through the radosgw and `s.ListObjects(plain, MaxKeys 1)` return the same `NextMarker`; record what it is for `_underscore.bin` (expected `__underscore.bin` if it is the first key; otherwise create a bucket holding `_a` and `b`, list with `max-keys 1`, and compare). If radosgw returns the escaped name, add a **Quirk** entry to `docs/ceph-upstream-bugs.md` ("ListObjects NextMarker for a key starting with `_` is the escaped index name") with the evidence lines [`rgw_rest_s3.cc:1962-1965`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_s3.cc#L1962-L1965) and [`rgw_rados.cc:1998-2001`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L1998-L2001), and an rgw-go line saying it reproduces it.
10. **Control-watch resilience.** `ceph osd down` is not available without breaking the cluster; instead assert `Store.Run` keeps running across a `rooket k rollout restart deploy/rook-ceph-rgw-...` of the radosgw (which does not touch the watches) and that the eight `notify.N` objects exist in the control pool (`rados -p ceph-objectstore.rgw.control ls` via the toolbox).

`hack/rooket/admin.sh <release> <radosgw-admin args...>` sources `lib.sh`, `use_release`, and runs `admin "$@"`; `cephtest.RadosgwAdmin` executes it with `exec.CommandContext`. The README gains one line for it.

- [ ] **Step 4: Both releases**

```sh
make cluster-up RELEASE=squid && make populate RELEASE=squid && make integration RELEASE=squid
make cluster-up RELEASE=tentacle && make populate RELEASE=tentacle && make integration RELEASE=tentacle
make gate RELEASE=squid && make gate RELEASE=tentacle   # the gate stays green
```

- [ ] **Step 5: Registry and exclusions**

`docs/ceph-upstream-bugs.md`: the **rgw-go** lines of "Squid does not guard listing-time index suggestions against resharding", "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards", "radosgw caches any control-pool UPDATE_OBJ notify payload unchecked" and "radosgw aborts the process after 100 failed control-watch re-registrations" gain the sentence "Realized in `internal/driver` (`list.go`'s `suggest`, `options.go`'s `floorShards` and `shardMod`, `cache.go`'s `onNotify`, `notify.go`'s `watchLoop`)." with the file names; the entry for #80992 corrects "unit G" to "unit M". `docs/exclusions.md`, "Coexistence obligations independent of any exclusion", gains the two differences this unit's startup carries (M-D1 and decision D4), appended as written:

```markdown
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
  data pools on first use. rgw-go never creates one: a missing root or
  control pool stops it at startup with an error naming the pool, and a
  request that needs another missing pool fails with an error naming it.
  Rook creates every pool a store names, or refuses a store whose named
  pools do not exist, before it starts a gateway (rook v1.20.7
  `pkg/operator/ceph/object/controller.go:620-654`,
  `objectstore.go:799-823`).
```

Report both changes for the rgw-rs session.

- [ ] **Step 6: Commit**

`feat(driver): assemble the metadata plane and prove it against the rooket clusters`, `docs: record how the metadata plane realizes the registry's directives and the startup differences it keeps`. Draft PR `feat: metadata plane assembly and cluster proof (unit M task 13)`. The PR description states what the cluster specs proved on each release and that OSD-outage watch recovery is verified on the fake only.

## Self-review

- **Spec coverage.** §9's phase-1 items owned by M: "ListBuckets" is G's op over M's `ListUserBuckets` (Task 6); "Create, Delete, Head and GetLocation for buckets" (Task 7); "ListObjects v1 and v2" (Task 8); the bucket half of "ACL, policy and tagging subresources" (Task 11); "cache invalidation both ways" (Tasks 2 and 3, proved on the cluster in Task 13 items 4 and 5); the listing half of "the reshard protocol" (Task 8's per-release `suggest`); "usage log writes" (Task 10); quota (Task 9). §8: every persistent write encodes at `s.release` (Tasks 3 to 11 pass it to `encodeAt` and the class calls), and the driver reproduces radosgw's placement of the root objects (1), control objects (2), user and index objects (4), bucket entry points and instances (5), owner bucket lists (6), index shards (6, 7) and usage objects (10). Unit M's goal sentence in the index (`00-index.md`, "The nine units") maps clause by clause onto Tasks 1 to 12; its gate onto Task 13. The registry directives: the notify payload is never applied (Task 3, `onNotify`), the watch loop backs off and never aborts (Task 2, `watchLoop`), the shard counts are floored and every modulo runs through `shardMod` (Task 12, used by Task 10), the listing suggestion is unguarded on Squid and guarded on Tentacle (Task 8, `suggest`), and the empty-`storage_classes` quirk stays an encoding matter: no M path consults `ZoneGroupPlacementTarget.StorageClasses` (placement validity is the zone placement's, `find_zone_placement`), so the phase-0 encoder's behaviour is untouched. Deliberately outside M, each named where it slots in: `search_realm_with_zone` (a Rook cluster has one realm, Task 1), old-format bucket conversion (Task 5), the multipart abort on bucket delete (P Task 7 Steps 6-8, which edit `DeleteBucket` in place at its comment), the multipart-part index sweep in `check_disk_state` (W Task 6 Steps 5-7, which edit `checkDiskState` in place at its comment), the account and group user indexes on `PutUser` (N, Task 4), `check_bucket_shards` (dynamic resharding, excluded, Task 9). The two startup behaviours that differ from radosgw, no zone bootstrap or master fix-up (M-D1, kept by owner decision 10) and no pool creation (decision D4), are recorded in `docs/exclusions.md` by Task 13, whose final `Open` realizes both.
- **Placeholder scan.** No `TBD`, no `TODO`, no "similar to", no unwritten step. Spec bodies written as a comment stating the fixture and the expected values, which the implementer expands into the `It` in the file's own style: Task 7's op-spec and handler-spec paragraphs, Task 8's "sends suggestions unguarded on Squid and guarded on Tentacle", Task 9's precedence, user-quota, account and owner-sync specs, Task 10's "logs nothing when rgw_enable_usage_log is false", Task 11's public-policy block and retry-count specs and its handler paragraph, Task 5's old-format entry point (which may become a `PIt` with the stated reason). Every one names its expected values and the C++ line it pins.
- **Type consistency.** `Store` fields used across tasks: `cluster`, `conf`, `release`, `zone *zoneConfig`, `pools *poolCache` (1); `sysobj *sysobjs`, `now func() time.Time` (3); `heads headStater`, `bgAIO *semaphore.Weighted` (8); `quota statsCaches` (9); `usage usageLogger`, `newTicker` (10); `opts options` (12); `nextBucketID atomic.Uint64` (7) — all created in Task 13's `Open`. `objv{read, write meta.ObjVersion}` is the tracker everywhere; `readParams{data, attrs, meta, objv}` and `readResult` are Task 3's names in Tasks 4 and 5; `entryPoint.v` and `instance.v` are values, read as `e.v.read`; `mapUserErr`/`mapBucketErr` map `ECANCELED` to `op.ErrConcurrentModification` and the rest through `op.FromRADOS` at their scope. The `fakerados` surface is one API grown task by task: `New`, `SetRequiredOSDRelease`, `SetConfig`, `RegisterClass`, `Object`, `Put`, `Watches`, `BreakWatches`, `FailWatch`, `FailNotify`, `Notifies`, `FailPool` (1); `Reads` (4); `Objects`, `Delete`, `AfterWrite` (7); `Writes`, `LastWrite`, `Suggestions` (8); `UsageEntries` (10); emulators `VersionClass` (3), `UserClass`, `RGWClass` (6, extended in 8 and 10). `meta` names used exist on main (`NewZone`, `NewIndexLayoutGen`, `NewBucketLayout`, `LogLayoutFromIndex`, `HashMod`, `IndexNormal`, `IndexIndexless`, `BucketInfo.IndexShardOID`, `IndexShard`, `ParsePool`, `ParseOwner`, `NewBucketEntryPoint`, `NewBucketInfo`, `NewUserInfo`, `NewAccountInfo`, `UserObject`, `UID`) or are added by this plan (`StrHashLinux`, `ParseIndexKeyName`, `ObjVersion`). `rgwcls` names (`NewDirEntry`, `DirEntryMeta.AppendableValue`, `PendingEntry{Tag, Info}`, `EntryVer{Pool int64, Epoch}`, `ListOp`, `ListRet`, `Suggestion`, `GuardBucketResharding`, `BucketInitIndex`, `GetDirHeader`, `UsageLogAdd`, `UsageLogEntry`, `UsageData`) and `user` names (`SetBucketsInfo`, `RemoveBucket`, `ListBuckets`, `GetHeader`, `CompleteStatsSync`, `Bucket`, `BucketEntry`, `Header`) are the phase-0 exports. `clsBucket` copies name, marker and id only: Rook buckets carry no explicit placement, which is the other half of `rgw_bucket::convert`. G's `op.ObjectEntry.Size` receives `accounted_size`, the number radosgw renders.
- **Review Focus.** 1 → Task 3 `never stores a notify payload: both ops invalidate and nothing is created`; 2 → Task 2 `retries past one hundred failures without stopping…` and `disables the cache when a watch is lost and re-enables it…`; 3 → Task 8 `fast-forwards a marker inside a common prefix` and `lists an object whose name starts with an underscore and skips the multipart namespace`; 4 → Task 7 `returns the existing record with BucketAlreadyExists and cleans up the losing instance` and `unlinks when the entry point was deleted concurrently`; 5 → Task 9 `fetches stats on a cold cache once…`, `uses the account's quotas for an account owner`, `compares rounded size by default and raw size when check_on_raw is set`.
- **Cluster tasks.** Only Task 13 touches a cluster, through `make cluster-up RELEASE=…` and the toolbox; every other task runs on `fakerados` and `memstore`. Recovery from a real OSD outage of the control watches is verified on the fake only; Task 13's PR description says so.
- **Contract findings for the caller** (not changed by this plan beyond the additive items in Global Constraints): (a) G Task 1 states `meta.ObjVersion` already exists; `main` at 0be608d has only `version.ObjVersion`, imported by `meta/cachenotify.go` — Task 3 adds it if G still has not (M-D9). (b) G's dispatch table gives bucket- and object-scope ACL requests the same route names, `get_acls` and `put_acls`, while `NewHandler` merges one handler map per unit; M registers both with a scope switch and two handler variables R and W assign (M-D8). (c) `op.ListObjectsParams` cannot express radosgw's non-standard `allow-unordered`; M adds `AllowUnordered` (M-D6). (d) `op.UsageEntry` has no payer, and radosgw keys requester-pays usage by the payer ([rgw_log.cc:159-165](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L159-L165)); M adds `Payer`. (e) G's `Op.Complete` has no shared usage logging; M adds `op.LogUsage` and calls it from every M op, and Task 7 Step 4 fills G's empty `ListBuckets.Complete` with `LogUsage(ctx, r, "list_buckets")`, since radosgw usage-logs every op including bucketless ones ([rgw_log.cc:195-224](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L195-L224), [:558-559](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_log.cc#L558-L559) at v19.2.6). (f) `docs/ceph-upstream-bugs.md`'s entry for tracker #80992 assigns the watch re-registration to "unit G"; it is M's (Task 13 corrects the line). (g) Z's contract needed nothing changed; `BuildDefaultACL` receives the identity's owner for both owner arguments on CreateBucket, and `ErrorFor` maps its parse errors.
