# Upstream Ceph defects

This registry records the defects in upstream ceph/ceph that rgw-go runs
into: in radosgw, the RGW object classes, librados, librbd, the monitors, or
the OSD behaviour they rely on. rgw-go must coexist with radosgw on the same
pools, so a defect in radosgw's behaviour is part of the compatibility
contract (section 8 of the design spec). The registry says, per defect:
- which releases carry it;
- whether rgw-go reproduces it, works around it, or is unaffected;
- where it stands upstream.

Each entry names its kind:

- **Defect** means upstream code does the wrong thing, whether or not
  upstream has fixed it in a later release.
- **Quirk** means the behaviour is intended upstream but surprises a
  reimplementation, for example a round trip that is not byte-identical.

Each entry records its evidence at a named release tag, how it was found,
and, under **Upstream**, its tracker issues and pull requests.
Every claim is verified against the source or a cluster, not taken from a
report. Add an entry whenever a task, review or benchmark hits an upstream
defect or quirk. Update it when an upstream issue is filed, when upstream
fixes it, or when rgw-go's handling changes. go-ceph's defects live in
`docs/cgo-limitations.md`, not here.

## Squid does not guard listing-time index suggestions against resharding

- **Kind:** defect, fixed in Tentacle.
- **Evidence:**
  - Listing sends `cls_rgw_suggest_changes` to repair stale index entries, and
    at v19.2.6 it sends them without the reshard guard (`rgw_rados.cc:9902`
    and `:10138`).
  - Ceph commit 461be1cd3d5, "rgw/rados: guard against dir suggest during
    reshard", adds the guard: "no changes to the bucket index should be
    allowed while resharding". It is in v20.0.0 and later (v20.2.4
    `rgw_rados.cc:10823` and `:11061`).
  - It is not on the squid branch as of a742f50 (2026-09-03).
- **Releases:** Squid, every release through v19.2.6.
- **rgw-go:** follows the cluster's release. Its Tentacle-level listing guards
  its suggestions; its Squid-level listing matches radosgw and does not.
  `docs/exclusions.md` states the guard per release.
- **Upstream:** [#81000](https://tracker.ceph.com/issues/81000) requests the
  squid backport of 461be1cd3d5.
- **Found:** final phase 0 review, 2026-09-26.

## check_disk_state removes a multipart part's index entry from the wrong shard

- **Kind:** defect, unfixed through main.
- **Evidence:** when a listing reconciles a head that exists, v19.2.6's
  `check_disk_state` walks the head's manifest and, for each location in the
  multipart namespace, calls `delete_obj_index` on an `rgw_obj` rebuilt by
  `raw_obj_to_obj` (`rgw_rados.cc:10413-10429`). That helper sets only the
  bucket and the key (`svc_tier_rados.h:131-143`), so the object's
  `index_hash_source` is empty and its shard is chosen by the part's own
  name (`get_hash_object`, `rgw_obj_types.h:540-541`). The part writer
  indexed the part under the upload's key instead
  (`head_obj.index_hash_source = target_obj.key.name`,
  `rgw_putobj_processor.cc:467`). On a bucket with more than one index
  shard the delete usually reaches a shard that holds no such entry, where
  `bucket_complete_op` logs "not on disk, no action" and changes nothing
  (`cls_rgw.cc:1135-1140`), leaving the part's entry behind. The same code is
  at v20.2.4 and on main (`rgw_putobj_processor.cc:501`). Derived from the
  source; not reproduced.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** unit W's listing reconciliation reproduces radosgw's bytes,
  wrong shard included, so the index a shared zone sees is the one radosgw
  would leave.
- **Upstream:** not filed; no tracker issue or pull request found.
- **Found:** phase 1 plan review, 2026-09-28; verified 2026-09-29.

## cls_rgw complete_op writes a stale epoch back when it cancels

- **Kind:** defect, a regression; unfixed through main.
- **Evidence:** at v19.2.6 `bucket_complete_op` turns a completion whose
  epoch is not newer than the entry's into a cancel (`cls_rgw.cc:1082-1086`),
  but first copies the op's version onto the entry (`:1094`), and the cancel
  branch writes the entry back (`:1115-1117`). The entry's `ver.epoch` falls
  back to the stale value, so a later completion whose epoch lies between the
  two passes the check and overwrites newer metadata: completions at epochs
  10, 5 and 7, in that order, leave 7's metadata indexed. The code is the same
  at v20.2.4 (`:1217-1229`) and on main (`:1228-1240`, 2026-09-28).
  - The same copy breaks radosgw's explicit cancel. `cls_obj_complete_cancel`
    sends pool -1 and epoch 0 (`rgw_rados.cc:9559-9561`), which resets the
    entry's version to -1:0; the next completion then fails the pool
    comparison and is applied whatever its epoch, so completion 10, a cancel,
    then completion 5 leaves 5's metadata indexed.
  - Reproduced on v19.2.6 and v20.2.4, with identical results, by direct
    class calls on a fresh index object: after completions 10 and 5 the entry
    holds 10's metadata under epoch 5, a completion at 7 then replaces it with
    7's, and the bucket header stats follow the index.
  - radosgw reaches it through racing PUTs of one key: the head write's epoch
    is the RADOS version it returns (`rgw_rados.cc:3300`, `:3320`),
    completions are sent asynchronously (`:9518`), and a failed write sends
    the cancel (`:3371-3374`).
  - It is a regression. Before 8b27472bbd8, "cls/rgw: index cancelation
    still cleans up remove_objs" (ceph/ceph#43854), the cancel branch wrote
    the entry back and returned before the copy (v16.2.7 `cls_rgw.cc:1017`
    and `:1019`, ahead of `entry.ver = op.ver` at `:1026`); that commit moved
    the copy above the cancel branch.
- **Releases:** v17.2.0 and later, and pacific from v16.2.12, which carries
  the backport 8c67a931c9e; checked at v19.2.6, v20.2.4 and main.
- **rgw-go:** meets it as radosgw does, because the class decides; no client
  can work around it. Whether rgw-go's own cancel also triggers the
  explicit-cancel case depends on the version it sends, which is phase 1's
  write path (unit W) to settle.
- **Upstream:** [#80894](https://tracker.ceph.com/issues/80894), filed
  2026-09-26 with the same analysis, both triggers and the regression from
  8b27472bbd8; its backports are tentacle and umbrella. Our
  [#81002](https://tracker.ceph.com/issues/81002) was closed as its
  duplicate. The fix under review is
  [ceph/ceph#72097](https://github.com/ceph/ceph/pull/72097);
  [ceph/ceph#72162](https://github.com/ceph/ceph/pull/72162), a second fix,
  was closed as a duplicate of it.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-008); verified 2026-09-27;
  reproduced 2026-09-28 on disposable Squid and Tentacle clusters.

## cls_rgw encodes a packed value of exactly 65536 as 0

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `encode_packed_val` uses the two-byte form for
  values up to and including 0x10000 and writes `(uint16_t)val`
  (`cls_rgw_types.h:272-275`), so 65536 is stored, and decodes, as 0. It
  encodes the pool and epoch of `rgw_bucket_entry_ver` (`:343-344`) and
  `index_ver` in index entries (`:408`) and bilog entries (`:623`). An index
  entry also stores `ver.epoch` raw (`:402`), but its decoder overwrites that
  with the packed copy (`:418` and `:426`). The bound dates from b1578ba705a
  (v0.67) and is unchanged on main (`cls_rgw_types.h:285`).
- **Releases:** every release.
- **rgw-go:** reproduces it for byte identity. `encodePacked` in
  `internal/cls/rgw/types_key.go` truncates 0x10000 to 0, and
  `fixtures_test.go` pins the bytes.
- **Upstream:** [#80995](https://tracker.ceph.com/issues/80995).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-009); verified 2026-09-27.

## Squid before 19.2.3 refuses a delete marker on top of a delete marker

- **Kind:** defect, fixed in v19.2.3 and v20.1.0.
- **Evidence:** at v19.2.2 `bucket_link_olh` answers ENOENT, and links
  nothing, for a delete marker on a new instance when the OLH already points
  at a delete marker (`cls_rgw.cc:1676-1692`). The check came with
  69d7589fb13 (v17.1.0; in Pacific from v16.2.6 as 1e575378b00) and is in
  every Reef release through v18.2.8. Ceph commit 65e3e9b5888, "rgw: revert
  PR #41897 to allow multiple delete markers to be created"
  (ceph/ceph#54957), removes it from v20.1.0 on; its backport 9cca4fd435a
  (ceph/ceph#62740) is in v19.2.3 and later.
- **Releases:** on Squid, v19.2.0 through v19.2.2, all below rgw-go's floor.
  The check runs in the OSD, so the OSD's release decides.
- **rgw-go:** unaffected. Every release it supports, v19.2.6 and later on
  Squid, carries the fix, so phase 2's versioning needs no handling for it.
- **Upstream:** fixed by
  [ceph/ceph#54957](https://github.com/ceph/ceph/pull/54957), and on squid by
  [ceph/ceph#62740](https://github.com/ceph/ceph/pull/62740); no tracker
  issue.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-007); verified 2026-09-27.

## cls_rgw usage trim never removes a payer-keyed record

- **Kind:** defect, fixed in v21.0.0 and deliberately not backported.
- **Evidence:** at v19.2.6 `user_usage_log_add` keys a record by its payer
  when it has one (`cls_rgw.cc:3583`), but the trim callback removes the keys
  built from the owner (`:3761`). A payer-keyed record in the trim range is
  therefore found on every call and never removed, so the call never answers
  ENODATA (`:3799-3800`); a trim by the payer removes the owner's record for
  the same hour and bucket instead, if there is one. radosgw repeats a trim
  until ENODATA with no bound (`rgw_rados.cc:10197-10211`), so
  `radosgw-admin usage trim` and the admin API's trim spin. Ceph commit
  674d42d9023 (ceph/ceph#65329) builds the keys from the payer; it is in
  v21.0.0 and later (v21.1.0 `cls_rgw.cc:4403`) and on neither the squid nor
  the tentacle branch.
- **Releases:** every Squid and Tentacle release, through v19.2.6 and v20.2.4.
- **rgw-go:** `internal/cls/rgw` marshals `user_usage_log_trim` but runs no
  trim loop yet. Phase 1's usage trim must bound its loop, since on Squid and
  Tentacle OSDs ENODATA may never come.
- **Upstream:** [#72593](https://tracker.ceph.com/issues/72593), fixed by
  [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329). Upstream decided
  not to backport the fix, because it changes how the usage log works; our
  backport request, [#80999](https://tracker.ceph.com/issues/80999), was
  closed as a duplicate of #72593.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-005); verified 2026-09-27.

## cls_rgw usage trim with a bucket filter stalls behind 1000 other records

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 each `user_usage_log_trim` call starts at the
  beginning of its range, since the request carries no marker
  (`cls_rgw.cc:3791`), scans at most 1000 keys (`:3794-3795`) and skips
  records of other buckets (`:3687-3688`). When those 1000 keys hold no record
  of the bucket and more keys follow, the call answers 0 rather than ENODATA
  (`:3799-3800`), and so does every later call, since nothing was removed.
  `radosgw-admin usage trim --bucket` trims by the bucket's owner and name
  (`rgw_sal_rados.cc:799-806`) through the unbounded loop of the previous
  entry, so it spins once the owner has more than 1000 records of other
  buckets in the range; records past them are never reached. The logic is
  unchanged at v20.2.4, v21.1.0 and main.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** as for the previous entry, phase 1's usage trim must bound its
  loop.
- **Upstream:** [#58136](https://tracker.ceph.com/issues/58136). Its fix,
  [ceph/ceph#49168](https://github.com/ceph/ceph/pull/49168), has been under
  review since 2022.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-006); verified 2026-09-27.

## radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards

- **Kind:** defect, unfixed through main.
- **Evidence:** `rgw_gc_max_objs`, `rgw_lc_max_objs` and `rgw_usage_max_shards`
  are plain `int`s with no `min:` and default 32 (v19.2.6 `rgw.yaml.in:1692`,
  `:427`, `:1515`), unlike the sibling `rgw_usage_max_user_shards`, which
  carries `min: 1` (`:1528-1540`). A configured 0 is accepted and then used as
  a divisor on the first request that shards:
  - GC: `RGWGC::tag_index` returns `rgw_shards_mod(hash, max_objs)`
    (`rgw_gc.cc:65`). At v19.2.2 `rgw_shards_mod` computes
    `hval % PRIME % max_shards` (`rgw_tools.h:45-52`), dividing by zero. From
    v19.2.3 (456a5e661d1) and v20.1.0 (a2b76b0e09e) it returns -1 for
    `max_shards <= 0`, so `tag_index` returns -1 and `send_chain` reads
    `obj_names[-1]` (`rgw_gc.cc:128-132`), out of bounds. GC enqueue runs on an
    overwrite or delete of a tailed object.
  - LC: `get_lc_index` computes `... % HASH_PRIME % max_objs` directly (v19.2.6
    `rgw_lc.cc:1974`), a divide-by-zero at every release that the
    `rgw_shards_mod` guard never covers; it runs from `guard_lc_modify`
    (`:2593`) on a bucket-lifecycle put (`:2636`) or delete (`:2668`).
  - Usage: `usage_log_hash` computes `val % max_shards` directly (v19.2.6
    `rgw_rados.cc:1624-1625`), a divide-by-zero at every release, from
    `log_usage` when usage logging is enabled and from usage read and trim.
  The same holds at v20.2.4 (`rgw_lc.cc:2031`, `rgw_rados.cc:1728-1729`, and
  GC's `obj_names[-1]` via `rgw_shards_mod` returning -1, `rgw_tools.h:63-71`).
- **Releases:** every release. GC faults as a SIGFPE at v19.2.2 and as an
  out-of-bounds read from v19.2.3 and v20.1.0 on; LC and usage fault as a
  SIGFPE throughout, since the `rgw_shards_mod` guard does not cover their
  direct modulo.
- **rgw-go:** config validation rgw-go must add. Phase 1 rejects or floors a
  zero `rgw_gc_max_objs` (GC worker, unit W), `rgw_lc_max_objs` and
  `rgw_usage_max_shards` (metadata and lifecycle path, unit M) at startup
  rather than faulting on first use, and never divides by a shard count without
  guarding it.
- **Upstream:** [#80991](https://tracker.ceph.com/issues/80991); its fix,
  [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160), adds min: 1 to
  the three options and is in review.
  [#75958](https://tracker.ceph.com/issues/75958), a crash when
  rgw_gc_max_objs changes at runtime, is related but has a different trigger.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-019); verified 2026-09-27.

## The 2pc queue's reserved size drifts upward

- **Kind:** defect, fixed.
- **Evidence:**
  - `cls_2pc_queue` reserve adds size+10 per entry, but commit, abort and
    expire subtract only size, so `reserved_size` grows until reservations
    fail with ENOSPC (v19.2.3 `cls_2pc_queue.cc:137` against `:300`, `:401`
    and `:543`).
  - The fix is two commits on each branch: b97fe168f62 and 8f86e0926f4 on
    squid, first released in v19.2.4; their backports 98ed23288db and
    7a8f84046b8 on tentacle, first released in v20.2.3; and 00ad83d3ab2 and
    7f4eaee30cb on main, in v21.0.0 and later.
  - The fix's one-time recompute can be skipped; see the next entry.
- **Releases:** v19.2.3 and earlier, v20.2.2 and earlier. Drift accrued
  before an upgrade can outlive it.
- **rgw-go:** never assumes `reserved_size` is exact. `docs/exclusions.md`
  records the accounting per release.
- **Upstream:** fixed by the commits above; no tracker issue.
- **Found:** reported by rgw-rs; verified against the tags in the phase 0
  final review. The per-branch fix commits were reported by rgw-rs (rados-rs
  CEPH-BUG-001) and verified 2026-09-27.

## The 2pc queue's self-heal is skipped when another write comes first

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 reserve recomputes `reserved_size` from the
  outstanding reservations only when the queue head's urgent data decoded
  below struct version 3 (`cls_2pc_queue.cc:135`), and every write re-encodes
  it at version 3 (`cls_2pc_queue_types.h:72`). Commit
  (`cls_2pc_queue.cc:371`), abort (`:458`), expiry (`:602`) and entry removal
  (`:698`) re-encode it without recomputing. If one of them is the first
  write to a drifted queue after the upgrade, the drift stays for good, and
  with it the spurious ENOSPC. The same holds at v20.2.4 (`:137`) and on
  main.
- **Releases:** v19.2.4 and later, v20.2.3 and later, and v21.0.0 and later,
  on a queue that drifted under an earlier release.
- **rgw-go:** has no 2pc queue client yet. Phase 3 runs the stale-reservation
  expiry on every persistent queue, which is one of the writes that keeps a
  drift, and must not assume `reserved_size` is exact (`docs/exclusions.md`).
- **Upstream:** [#80994](https://tracker.ceph.com/issues/80994). A
  maintainer's comment there calls it a duplicate of the drift fix for
  [#74713](https://tracker.ceph.com/issues/74713), but that fix is what added
  the reserve-only recompute, and every branch still recomputes only in
  reserve.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-002); verified 2026-09-27.

## The 2pc queue hands out reservation id 0, which radosgw treats as none

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 a reservation id is a u32 whose value 0 is `NO_ID`
  (`cls_2pc_queue_types.h:9-10`), and reserve pre-increments `last_id`
  without skipping 0 (`cls_2pc_queue.cc:186`), so the reservation after 2^32
  on one queue gets id 0. radosgw's publisher skips both the commit and the
  abort of a reservation whose id is `NO_ID` (`rgw_notify.cc:1173-1174` and
  `:1283-1284`): the event is lost, and its space stays reserved until the
  expiry removes it. The same holds at v20.2.4 and on main. Derived from the
  source; not reproduced.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** has no notification publisher yet. Phase 3's must not use id 0
  as "no reservation"; the class commits id 0 like any other.
- **Upstream:** [#80996](https://tracker.ceph.com/issues/80996); its fix,
  [ceph/ceph#72163](https://github.com/ceph/ceph/pull/72163), skips NO_ID when
  the id wraps and is in review.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-003); verified 2026-09-27.

## radosgw's notification queue listing never pages past 1024 queues

- **Kind:** defect, fixed in v20.2.3 and v21.0.0, not on squid.
- **Evidence:** at v19.2.6 `read_queue_list` reads the queue registry's omap
  keys 1024 at a time but never advances `start_after` (`rgw_notify.cc:108`,
  used at `:114`), so with more than 1024 queues registered it re-reads the
  first page for ever. Ceph commit b984980897d, "rgw/notify: fix reading the
  entries in a loop" (ceph/ceph#66246), advances it and cites tracker #73812;
  it is in v21.0.0 and later, and its backport 15de1799510 (ceph/ceph#66491)
  is in v20.2.3 and later (v20.2.4 `rgw_notify.cc:130`). It is not on the
  squid branch as of a742f50 (2026-09-03).
- **Releases:** every Squid release through v19.2.6; v20.2.0 through v20.2.2.
- **rgw-go:** cannot fix a coexisting radosgw; `docs/exclusions.md` records
  the hazard. Phase 3's own reading of the registry must advance its marker.
- **Upstream:** [#73812](https://tracker.ceph.com/issues/73812). Its squid
  backport, [#73893](https://tracker.ceph.com/issues/73893), is under review;
  the tentacle backport, [#73894](https://tracker.ceph.com/issues/73894), is
  resolved.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-004); verified 2026-09-27.
  `docs/exclusions.md` already recorded it.

## radosgw caches any control-pool UPDATE_OBJ notify payload unchecked

- **Kind:** quirk. The cache-coherence protocol trusts intra-cluster notifies
  by design; the surprise is that read access to the control pool is enough to
  plant a cache entry.
- **Evidence:** at v19.2.6 `RGWSI_SysObj_Cache::watch_cb`
  (`svc_sys_obj_cache.cc:465`) decodes the notify's `RGWCacheNotifyInfo` and,
  on `UPDATE_OBJ`, stores its `obj_info` straight into the cache (`:490-491`),
  checking neither a signature nor the `notifier_id` it is passed (`:468`).
  `RGWWatcher::handle_notify` runs the callback and acks unconditionally
  (`svc_notify.cc:77` and `:80`). A RADOS notify is a read op (`rados.h:258`,
  `NOTIFY = __CEPH_OSD_OP(RD, DATA, 6)`), so read caps on the zone's control
  pool are enough to send one; a planted entry lives
  `rgw_cache_expiry_interval` (default 900 s, `rgw.yaml.in:3324`). The same at
  v20.2.4 (`svc_sys_obj_cache.cc:490-491`).
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** phase 1's cache-notify handling (unit M) must treat a control
  notify as an invalidate-and-reread and never apply the notify payload as
  truth, and must keep its cephx caps on the control pool narrow.
  `docs/exclusions.md` records cache invalidation as a coexistence obligation.
- **Upstream:** not filed; the behaviour is radosgw's design.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-017); verified 2026-09-27.

## radosgw aborts the process after 100 failed control-watch re-registrations

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `RGWWatcher::reinit` calls `abort()` once
  `retries > 100` (`svc_notify.cc:89-93`). `retries` is a per-watcher counter
  initialised to 0 (`:36`) and incremented on every failed unregister or
  register (`:103`, `:111`); nothing resets it, so it counts failures over the
  whole process lifetime, and each failure reschedules `reinit` at once with no
  backoff (`handle_error` → `C_ReinitWatch`, `:82-86`). The counter and abort
  arrived with ff248d7ed94, "rgw: Try to handle unwatch errors sensibly", first
  released in v19.2.3, and its main-line twin 34366f0f0d8, first released in
  v20.1.0; v19.2.2's `reinit` has no counter and reschedules for ever
  (`:88-101`). The same at v20.2.4 (`svc_notify.cc:87-91`). The default
  `rados_osd_op_timeout` is 0 (`global.yaml.in:6379`), so a watch op blocks
  rather than fails during a transient OSD outage and does not by itself reach
  the counter.
- **Releases:** v19.2.3 and later, v20.1.0 and later, through v19.2.6, v20.2.4
  and main; v19.2.2 and earlier do not abort.
- **rgw-go:** phase 1's driver control-watch re-registration (unit M) must back
  off between attempts and retry without ever aborting the process, and must
  not count re-registration failures unboundedly over the process lifetime.
- **Upstream:** [#80992](https://tracker.ceph.com/issues/80992).
  [ceph/ceph#68207](https://github.com/ceph/ceph/pull/68207) (2026-04) reset
  the counter on success, among other reinit fixes, but the stale bot closed
  it unreviewed; it was filed for
  [#73564](https://tracker.ceph.com/issues/73564), a different symptom (a
  watch2 segfault).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-018); verified 2026-09-27.

## radosgw adds STANDARD to an empty placement target on decode

- **Kind:** quirk.
- **Evidence:** at v19.2.6 the binary decoder of a zonegroup placement target
  inserts `STANDARD` when `storage_classes` is empty (`rgw_zone_types.h:624`),
  and the JSON decoder does the same (`rgw_zone.cc:757`). So a zonegroup that
  radosgw decodes and re-encodes is longer than the stored bytes: 448 bytes
  against 436 on the phase 0 cluster. ceph-dencoder round-trips the stored
  bytes unchanged.
- **Releases:** Squid and Tentacle.
- **rgw-go:** encodes what is stored, matching ceph-dencoder. The phase 0
  gate keeps a ceph-dencoder oracle for a zonegroup that hits this case; since
  populate added a cloud-tier storage class, the phase 0 clusters no longer
  do.
- **Upstream:** not filed; a quirk.
- **Found:** phase 0 gate, Task 15.

## cls_version's header documents EAGAIN, but the class returns ECANCELED

- **Kind:** quirk. ECANCELED is the code radosgw relies on; the header
  comment is wrong.
- **Evidence:** at v19.2.6 `cls_version_client.h:19` says a conditional
  `inc` returns EAGAIN when its condition fails, but `inc` and `check` both
  return ECANCELED (`cls_version.cc:166` and `:196`). The same at v20.2.4,
  v21.1.0 and main.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** follows the class. `internal/cls/version` documents
  `ErrCanceled` for `IncConds` and `Check`, and its integration spec asserts
  it.
- **Upstream:** not filed; the header comment is wrong, not the class.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-014); verified 2026-09-27.

## cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `read_lock` removes the object when an ephemeral
  lock has no unexpired holder left (`cls_lock.cc:93-98`). `get_info` and
  `assert_locked` are registered without the write flag (`:634-642`), and the
  OSD fails a class call that writes without it with EIO
  (`PrimaryLogPG.cc:6181-6184`). So both answer EIO rather than the lock's
  state, and the removal is dropped with them. Ephemeral locks date from
  a289f2d8654 (v14.1.0); both methods are still read-only on main
  (`cls_lock_ops.h:255` and `:257`).
- **Releases:** every release since v14.1.0; checked at v19.2.6, v20.2.4 and
  main.
- **rgw-go:** unaffected; it has no cls_lock client yet. radosgw's one
  ephemeral lock, the bucket reshard lock (`rgw_reshard.cc:734`), is never
  read with either method; rgw-go must do the same if it takes that lock.
  `docs/exclusions.md` records the failure mode.
- **Upstream:** [#80993](https://tracker.ceph.com/issues/80993). Upstream QA
  logged the failure in 2022 as
  [#56575](https://tracker.ceph.com/issues/56575), which was resolved by a
  test-only change (d3457c64b1b).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-011); verified 2026-09-27.

## cls_otp divides by a stored step_size that is never validated

- **Kind:** defect, unfixed through main.
- **Evidence:** `otp_instance`'s verify path computes
  `index = result + (secs - time_ofs) / step_size` (v19.2.6
  `cls_otp.cc:143`, main `:139`), where `step_size` is a `uint32_t` a caller
  can store as 0. `otp_set` writes the decoded value with no check
  (`cls_otp.cc:301-347`). liboath does not stop the division: given a zero
  time step, `oath_totp_validate4_callback` substitutes 30
  (`liboath/totp.c:542-543`) and validates a correct code, so a non-negative
  result reaches the division and the OSD faults on the integer divide by
  zero.
- **Trigger:** privileged only. Storing `step_size == 0` needs direct RADOS
  write caps on the OTP pool (`<zone>.rgw.otp`, `svc_otp.cc:22-24`), or the
  RGW admin cap `metadata=write` via `metadata put otp:<uid>`, whose
  `otp_info_t::decode_json` reads `step_size` with no `> 0` guard
  (`cls_otp_types.cc:69`). `radosgw-admin mfa create` defaults it to 30
  (`rgw_admin.cc:10824-10825`), and no S3 end-user path stores an OTP entry.
  So the effect is a denial of service (an OSD SIGFPE that recurs on each
  check until the entry is cleared) reachable only by a cluster-service or
  admin actor, not a cross-privilege escalation.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** no cls_otp client yet (phase 2, MFA). When it writes OTP
  entries it must reject a zero `step_size`, and it must not divide by a
  stored `step_size` without guarding it.
- **Upstream:** [#80948](https://tracker.ceph.com/issues/80948).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-012); liboath premise and
  trigger verified 2026-09-27.

## cls_otp computes the replay index from an unsigned window distance

- **Kind:** defect, unfixed through main.
- **Evidence:** verify calls `oath_totp_validate2(..., window, nullptr, otp)`
  (v19.2.6 `cls_otp.cc:133-136`), discarding the signed position
  out-parameter, and sets the replay index to the return value plus the
  current step (`:143`), guarding with `if (index <= last_success)` (`:145`)
  and storing `last_success = index` (`:150`). liboath's return value is the
  absolute window distance in both directions; the sign is carried only by
  the discarded `otp_pos` (`liboath/totp.c:547-596`, and the documented
  contract "Returns absolute value of position in OTP window"). A code
  matched `iter` steps in the past therefore records an index `2*iter` steps
  ahead of the true step, so replay handling misfires: legitimate codes for
  the intervening steps are refused, and an earlier code can be accepted
  after a later one within the window. It does not bypass MFA.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** no cls_otp client yet (phase 2, MFA). If it reproduces the
  replay index it must take the direction from the position out-parameter,
  not from the return value.
- **Upstream:** [#80949](https://tracker.ceph.com/issues/80949).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-013); liboath premise
  verified 2026-09-27.

## librbd leaks the update-watch context when registration fails

- **Kind:** defect, currently unreachable.
- **Evidence:** at v19.2.6 `rbd_update_watch` allocates a `C_UpdateWatchCB`
  and returns it as the handle whatever `register_update_watcher` returns
  (`librbd.cc:6887-6897`). Only `rbd_update_unwatch` frees it, so a failed
  registration would leak it. `register_update_watcher` returns 0 on every
  path from v19.2.3 through v20.3.0, so the failure cannot happen today.
- **Releases:** every release checked, v19.2.3 through v20.3.0.
- **rgw-go:** unaffected; it does not use rbd. go-ceph's side of the same
  path is ceph/go-ceph#1342.
- **Upstream:** not filed; the leak is unreachable today. go-ceph's side is
  [ceph/go-ceph#1342](https://github.com/ceph/go-ceph/pull/1342).
- **Found:** go-ceph upstreaming audit, 2026-09-27.

## The monitor's default for insecure key creation lags auth_allowed_ciphers

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 the default of `mon_auth_allow_insecure_key` is
  derived from the monmap's `auth_allowed_ciphers` only in the AuthMonitor's
  `check_health` (`AuthMonitor.cc:482` and `:506-508`), which runs from the
  leader's tick (`:172-184`, every `mon_tick_interval`, 5 s) and when it
  encodes an auth proposal (`:479`). So an insecure key type is refused
  (`:1534-1537`) right after `ceph mon set auth_allowed_ciphers` allows it,
  which is a monmap change, and on a newly elected leader, until the next
  tick. The option's description says the default follows the allowed
  ciphers (`mon.yaml.in:769-781`). The same at v20.2.4 and main. Derived from
  the source; not observed on a cluster.
- **Releases:** v19.2.6, v20.2.4, v21.1.1 and main; earlier releases lack the
  key-type switch.
- **rgw-go:** unaffected; it sends no auth commands. Its only monitor command
  is `osd dump`.
- **Upstream:** [#80997](https://tracker.ceph.com/issues/80997).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-015); verified 2026-09-27.

## The monitor reports a refused cephx key type as EINVAL

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `get_cipher_type` answers EINVAL for an unknown
  key type, ENOTSUP for AES256KRB5 without the monitor feature, and EPERM for
  a type that policy refuses (`AuthMonitor.cc:1510-1547`). Each of its five
  callers, `auth add`, the pending-key commands, `auth get-or-create[-key]`,
  `fs authorize` and `auth rotate`, replaces that code with EINVAL
  (`:1644-1646`, `:1740-1742`, `:1824-1826`, `:1907-1909` and
  `:2070-2072`), so only the status text tells a refusal from a bad
  argument. The same at v20.2.4 and main.
- **Releases:** v19.2.6, v20.2.4, v21.1.1 and main; earlier releases lack the
  key-type switch.
- **rgw-go:** unaffected; it sends no auth commands.
- **Upstream:** [#80998](https://tracker.ceph.com/issues/80998).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-016); verified 2026-09-27.
