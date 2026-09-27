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

Each entry records its evidence at a named release tag, and how it was found.
Every claim is verified against the source or a cluster, not taken from a
report. Add an entry whenever a task, review or benchmark hits an upstream
defect or quirk. Update it when upstream fixes it or rgw-go's handling
changes. go-ceph's defects live in `docs/cgo-limitations.md`, not here.

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
- **Found:** final phase 0 review, 2026-09-26.

## cls_rgw complete_op writes a stale epoch back when it cancels

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `bucket_complete_op` turns a completion whose
  epoch is not newer than the entry's into a cancel (`cls_rgw.cc:1082-1086`),
  but first copies the op's version onto the entry (`:1094`), and the cancel
  branch writes the entry back (`:1115-1117`). The entry's `ver.epoch` falls
  back to the stale value, so a later completion whose epoch lies between the
  two passes the check and overwrites newer metadata: completions at epochs
  10, 5 and 7, in that order, leave 7's metadata indexed. The code is the same
  at v20.2.4 (`:1217-1229`) and on main. The overwrite is derived from the
  source and has not been reproduced.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** meets it as radosgw does, because the class decides; no client
  can work around it. The data path that sends completions arrives in
  phase 1.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-008); verified 2026-09-27.

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
- **Releases:** within rgw-go's range, v19.2.0 through v19.2.2. The check
  runs in the OSD, so the OSD's release decides.
- **rgw-go:** has no OLH client yet. Versioning, in phase 2, must expect this
  ENOENT from OSDs at v19.2.0 to v19.2.2 and handle it as radosgw of that
  release does.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-007); verified 2026-09-27.

## cls_rgw usage trim never removes a payer-keyed record

- **Kind:** defect, fixed in v21.0.0 and not backported.
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
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-006); verified 2026-09-27.

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
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-004); verified 2026-09-27.
  `docs/exclusions.md` already recorded it.

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
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-012); liboath premise and
  trigger verified 2026-09-27. Unreported upstream as of the 2026-09-27
  tracker search; a public tracker issue is to be filed.

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
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-013); liboath premise
  verified 2026-09-27. Unreported upstream as of the 2026-09-27 tracker
  search; a public tracker issue is to be filed.

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
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-016); verified 2026-09-27.
