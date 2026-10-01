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

| Entry | Upstream issues | Upstream fix PRs | Found by us |
| --- | --- | --- | --- |
| [Squid does not guard listing-time index suggestions against resharding](#squid-does-not-guard-listing-time-index-suggestions-against-resharding) | [#81000](https://tracker.ceph.com/issues/81000), [#81217](https://tracker.ceph.com/issues/81217) | [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609), [ceph/ceph#72254](https://github.com/ceph/ceph/pull/72254) |  |
| [check_disk_state removes a multipart part's index entry from the wrong shard](#check_disk_state-removes-a-multipart-parts-index-entry-from-the-wrong-shard) | [#81121](https://tracker.ceph.com/issues/81121) | none | ✓ |
| [cls_rgw complete_op writes a stale epoch back when it cancels](#cls_rgw-complete_op-writes-a-stale-epoch-back-when-it-cancels) | [#80894](https://tracker.ceph.com/issues/80894), [#81002](https://tracker.ceph.com/issues/81002) | [ceph/ceph#72097](https://github.com/ceph/ceph/pull/72097) |  |
| [cls_rgw encodes a packed value of exactly 65536 as 0](#cls_rgw-encodes-a-packed-value-of-exactly-65536-as-0) | [#80995](https://tracker.ceph.com/issues/80995) | none | ✓ |
| [Squid before 19.2.3 refuses a delete marker on top of a delete marker](#squid-before-1923-refuses-a-delete-marker-on-top-of-a-delete-marker) | none | [ceph/ceph#54957](https://github.com/ceph/ceph/pull/54957), [ceph/ceph#62740](https://github.com/ceph/ceph/pull/62740) |  |
| [cls_rgw usage trim never removes a payer-keyed record](#cls_rgw-usage-trim-never-removes-a-payer-keyed-record) | [#72593](https://tracker.ceph.com/issues/72593), [#80999](https://tracker.ceph.com/issues/80999) | [ceph/ceph#65329](https://github.com/ceph/ceph/pull/65329) |  |
| [cls_rgw usage trim with a bucket filter stalls behind 1000 other records](#cls_rgw-usage-trim-with-a-bucket-filter-stalls-behind-1000-other-records) | [#58136](https://tracker.ceph.com/issues/58136) | [ceph/ceph#49168](https://github.com/ceph/ceph/pull/49168) |  |
| [radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards](#radosgw-faults-on-a-zero-rgw_gc_max_objs-rgw_lc_max_objs-or-rgw_usage_max_shards) | [#80991](https://tracker.ceph.com/issues/80991) | [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160) | ✓ |
| [radosgw truncates a long aws-chunked trailer section instead of rejecting it](#radosgw-truncates-a-long-aws-chunked-trailer-section-instead-of-rejecting-it) | [#81122](https://tracker.ceph.com/issues/81122) | none | ✓ |
| [radosgw accepts a negative or overflowing aws-chunked chunk size](#radosgw-accepts-a-negative-or-overflowing-aws-chunked-chunk-size) | [#81123](https://tracker.ceph.com/issues/81123) | none | ✓ |
| [radosgw writes ACL owner and grantee names into its XML unescaped](#radosgw-writes-acl-owner-and-grantee-names-into-its-xml-unescaped) | none | none | ✓ |
| [radosgw ignores a payload-hash mismatch on bodies read by read_all_input](#radosgw-ignores-a-payload-hash-mismatch-on-bodies-read-by-read_all_input) | [#81230](https://tracker.ceph.com/issues/81230) | [ceph/ceph#72263](https://github.com/ceph/ceph/pull/72263) | ✓ |
| [radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned](#radosgw-1926-and-2024-reject-a-sigv4-request-whose-content-type-is-unsigned) | [#79674](https://tracker.ceph.com/issues/79674), [#79708](https://tracker.ceph.com/issues/79708), [#79723](https://tracker.ceph.com/issues/79723), [#79725](https://tracker.ceph.com/issues/79725), [#79724](https://tracker.ceph.com/issues/79724) | [ceph/ceph#71192](https://github.com/ceph/ceph/pull/71192), [ceph/ceph#71296](https://github.com/ceph/ceph/pull/71296), [ceph/ceph#71364](https://github.com/ceph/ceph/pull/71364), [ceph/ceph#71363](https://github.com/ceph/ceph/pull/71363) |  |
| [radosgw checks a copy source with inputs from the destination bucket](#radosgw-checks-a-copy-source-with-inputs-from-the-destination-bucket) | [#81248](https://tracker.ceph.com/issues/81248) | [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) | ✓ |
| [radosgw terminates on a copy source or system request whose bucket policy does not parse](#radosgw-terminates-on-a-copy-source-or-system-request-whose-bucket-policy-does-not-parse) | [#81253](https://tracker.ceph.com/issues/81253) | [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) | ✓ |
| [radosgw checks a CopyObject source against its bucket's ACL, not the object's](#radosgw-checks-a-copyobject-source-against-its-buckets-acl-not-the-objects) | none | none | ✓ |
| [Squid accepts RestrictPublicBuckets but never enforces it](#squid-accepts-restrictpublicbuckets-but-never-enforces-it) | [#65741](https://tracker.ceph.com/issues/65741), [#70860](https://tracker.ceph.com/issues/70860), [#70859](https://tracker.ceph.com/issues/70859) | [ceph/ceph#57206](https://github.com/ceph/ceph/pull/57206) |  |
| [radosgw answers 200 to a CreateBucket that loses a race to another owner](#radosgw-answers-200-to-a-createbucket-that-loses-a-race-to-another-owner) | [#76398](https://tracker.ceph.com/issues/76398) | [ceph/ceph#68722](https://github.com/ceph/ceph/pull/68722) |  |
| [radosgw's admin API bypass-gc removal leaks a tail and runs unbounded](#radosgws-admin-api-bypass-gc-removal-leaks-a-tail-and-runs-unbounded) | none | none | ✓ |
| [radosgw's bypass-gc bucket removal fails once a tail stripe is gone](#radosgws-bypass-gc-bucket-removal-fails-once-a-tail-stripe-is-gone) | [#24789](https://tracker.ceph.com/issues/24789), [#40587](https://tracker.ceph.com/issues/40587) | [ceph/ceph#28789](https://github.com/ceph/ceph/pull/28789), [ceph/ceph#30198](https://github.com/ceph/ceph/pull/30198), [ceph/ceph#29984](https://github.com/ceph/ceph/pull/29984), [ceph/ceph#29956](https://github.com/ceph/ceph/pull/29956) |  |
| [The 2pc queue's reserved size drifts upward](#the-2pc-queues-reserved-size-drifts-upward) | none | none |  |
| [The 2pc queue's self-heal is skipped when another write comes first](#the-2pc-queues-self-heal-is-skipped-when-another-write-comes-first) | [#80994](https://tracker.ceph.com/issues/80994) | none | ✓ |
| [The 2pc queue hands out reservation id 0, which radosgw treats as none](#the-2pc-queue-hands-out-reservation-id-0-which-radosgw-treats-as-none) | [#80996](https://tracker.ceph.com/issues/80996) | [ceph/ceph#72163](https://github.com/ceph/ceph/pull/72163) | ✓ |
| [radosgw's notification queue listing never pages past 1024 queues](#radosgws-notification-queue-listing-never-pages-past-1024-queues) | [#73812](https://tracker.ceph.com/issues/73812), [#73893](https://tracker.ceph.com/issues/73893), [#73894](https://tracker.ceph.com/issues/73894) | [ceph/ceph#66246](https://github.com/ceph/ceph/pull/66246), [ceph/ceph#66345](https://github.com/ceph/ceph/pull/66345), [ceph/ceph#66491](https://github.com/ceph/ceph/pull/66491) |  |
| [Squid's realm reload hangs when a pubsub HTTP push has lost its wakeup](#squids-realm-reload-hangs-when-a-pubsub-http-push-has-lost-its-wakeup) | none | [ceph/ceph#57632](https://github.com/ceph/ceph/pull/57632) |  |
| [radosgw's realm reload waits out the notification manager's timers](#radosgws-realm-reload-waits-out-the-notification-managers-timers) | [#71963](https://tracker.ceph.com/issues/71963), [#72004](https://tracker.ceph.com/issues/72004), [#72003](https://tracker.ceph.com/issues/72003) | [ceph/ceph#63986](https://github.com/ceph/ceph/pull/63986) |  |
| [radosgw caches any control-pool UPDATE_OBJ notify payload unchecked](#radosgw-caches-any-control-pool-update_obj-notify-payload-unchecked) | none | none |  |
| [radosgw aborts the process after 100 failed control-watch re-registrations](#radosgw-aborts-the-process-after-100-failed-control-watch-re-registrations) | [#80992](https://tracker.ceph.com/issues/80992) | [ceph/ceph#72251](https://github.com/ceph/ceph/pull/72251) |  |
| [radosgw adds STANDARD to an empty placement target on decode](#radosgw-adds-standard-to-an-empty-placement-target-on-decode) | none | none |  |
| [radosgw lets a matching referer grant replace the requester's own ACL grants](#radosgw-lets-a-matching-referer-grant-replace-the-requesters-own-acl-grants) | none | none | ✓ |
| [radosgw ends a Referer's userinfo at an @ in its path](#radosgw-ends-a-referers-userinfo-at-an--in-its-path) | none | none | ✓ |
| [cls_version's header documents EAGAIN, but the class returns ECANCELED](#cls_versions-header-documents-eagain-but-the-class-returns-ecanceled) | none | none |  |
| [cls_lock get_info and assert_locked fail with EIO on an expired ephemeral lock](#cls_lock-get_info-and-assert_locked-fail-with-eio-on-an-expired-ephemeral-lock) | [#80993](https://tracker.ceph.com/issues/80993), [#56575](https://tracker.ceph.com/issues/56575) | none |  |
| [cls_otp divides by a stored step_size that is never validated](#cls_otp-divides-by-a-stored-step_size-that-is-never-validated) | [#80948](https://tracker.ceph.com/issues/80948) | [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250) | ✓ |
| [cls_otp computes the replay index from an unsigned window distance](#cls_otp-computes-the-replay-index-from-an-unsigned-window-distance) | [#80949](https://tracker.ceph.com/issues/80949) | [ceph/ceph#72253](https://github.com/ceph/ceph/pull/72253) | ✓ |
| [librbd leaks the update-watch context when registration fails](#librbd-leaks-the-update-watch-context-when-registration-fails) | none | none |  |
| [The monitor's default for insecure key creation lags auth_allowed_ciphers](#the-monitors-default-for-insecure-key-creation-lags-auth_allowed_ciphers) | [#80997](https://tracker.ceph.com/issues/80997) | none | ✓ |
| [The monitor reports a refused cephx key type as EINVAL](#the-monitor-reports-a-refused-cephx-key-type-as-einval) | [#80998](https://tracker.ceph.com/issues/80998) | none | ✓ |
| [The --name error lists the entity types as raw bytes](#the---name-error-lists-the-entity-types-as-raw-bytes) | [#79678](https://tracker.ceph.com/issues/79678), [#79640](https://tracker.ceph.com/issues/79640), [#79638](https://tracker.ceph.com/issues/79638) | [ceph/ceph#71165](https://github.com/ceph/ceph/pull/71165), [ceph/ceph#71190](https://github.com/ceph/ceph/pull/71190), [ceph/ceph#71191](https://github.com/ceph/ceph/pull/71191) |  |
| [radosgw ignores a bad port in an IPv6 endpoint](#radosgw-ignores-a-bad-port-in-an-ipv6-endpoint) | none | none | ✓ |
| [Squid's radosgw fails to start when its realm search meets a realm whose period cannot be read](#squids-radosgw-fails-to-start-when-its-realm-search-meets-a-realm-whose-period-cannot-be-read) | none | [ceph/ceph#63266](https://github.com/ceph/ceph/pull/63266), [ceph/ceph#66300](https://github.com/ceph/ceph/pull/66300) |  |
| [radosgw-admin bucket rm exits 0 when it removes nothing](#radosgw-admin-bucket-rm-exits-0-when-it-removes-nothing) | none | none | ✓ |
| [radosgw's S3 ListBuckets reads neither max-buckets nor continuation-token](#radosgws-s3-listbuckets-reads-neither-max-buckets-nor-continuation-token) | [#72315](https://tracker.ceph.com/issues/72315), [#75463](https://tracker.ceph.com/issues/75463) | [ceph/ceph#64742](https://github.com/ceph/ceph/pull/64742), [ceph/ceph#67920](https://github.com/ceph/ceph/pull/67920) |  |
| [url_decode reads outside its hex table for a byte above 0x7f after "%"](#url_decode-reads-outside-its-hex-table-for-a-byte-above-0x7f-after-) | [#4755](https://tracker.ceph.com/issues/4755), [#81267](https://tracker.ceph.com/issues/81267) | [ceph/ceph#72272](https://github.com/ceph/ceph/pull/72272) |  |
| [A malformed percent-escape in the path makes radosgw serve another path](#a-malformed-percent-escape-in-the-path-makes-radosgw-serve-another-path) | [#71458](https://tracker.ceph.com/issues/71458), [#81301](https://tracker.ceph.com/issues/81301) | [ceph/ceph#72301](https://github.com/ceph/ceph/pull/72301) |  |
| [radosgw skips or never finishes every bucket-index batch on a zero rgw_bucket_index_max_aio](#radosgw-skips-or-never-finishes-every-bucket-index-batch-on-a-zero-rgw_bucket_index_max_aio) | none | none | ✓ |
| [radosgw clamps a copy of rgw_override_bucket_index_max_shards that bucket creation never reads](#radosgw-clamps-a-copy-of-rgw_override_bucket_index_max_shards-that-bucket-creation-never-reads) | [#70980](https://tracker.ceph.com/issues/70980) | [ceph/ceph#72256](https://github.com/ceph/ceph/pull/72256) |  |
| [radosgw wraps an rgw_cache_expiry_interval above 18446744073 seconds](#radosgw-wraps-an-rgw_cache_expiry_interval-above-18446744073-seconds) | [#81218](https://tracker.ceph.com/issues/81218) | [ceph/ceph#72255](https://github.com/ceph/ceph/pull/72255) | ✓ |
| [radosgw spins or stops caching on a negative usage-log or quota interval](#radosgw-spins-or-stops-caching-on-a-negative-usage-log-or-quota-interval) | [#81226](https://tracker.ceph.com/issues/81226) | [ceph/ceph#72261](https://github.com/ceph/ceph/pull/72261) | ✓ |
| [radosgw's ARN conditions compare each ARN component with the text after it](#radosgws-arn-conditions-compare-each-arn-component-with-the-text-after-it) | none | none | ✓ |
| [radosgw takes any policy Action starting with a wildcard for every action](#radosgw-takes-any-policy-action-starting-with-a-wildcard-for-every-action) | [#81229](https://tracker.ceph.com/issues/81229) | [ceph/ceph#72262](https://github.com/ceph/ceph/pull/72262) | ✓ |

A ✓ under Found by us marks a defect first found by the project's own sessions, the repository owner's Claude Code sessions such as rgw-go, rgw-rs and rgw-bug-reproduction, with no earlier upstream report or fix PR.

Every new entry adds its row to this table, in document order.

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
- **Upstream:** [#81000](https://tracker.ceph.com/issues/81000), which we
  filed, requests the squid backport of 461be1cd3d5, which came with
  [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609). The backport,
  [ceph/ceph#72254](https://github.com/ceph/ceph/pull/72254), a draft, is a
  clean cherry-pick of 461be1cd3d5 that keeps Casey Bodley as its author. It
  is open, and its backport audit fails: the audit needs a source tracker for
  ceph/ceph#59609, which merged in 2024 without one.
  [#81217](https://tracker.ceph.com/issues/81217), created for
  ceph/ceph#59609 with Backport set to squid, now supplies that link, and a
  comment on ceph/ceph#72254 points to it. Moving #81217 to Pending Backport
  and rerunning or overriding the audit are reserved to the Ceph release
  team, so the audit stays red until they act.
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
  at v20.2.4 and on main (`rgw_putobj_processor.cc:501`).
  - Reproduced on v19.2.6 and v20.2.4 with a three-part upload on an
    11-shard bucket: each part's delete went to the shard its own name
    hashes to and logged "not on disk, no action", and the entries stayed
    on the upload's shard. On a bucket resharded to one shard every entry
    was removed. Each part misses on its own: its delete reaches the
    upload's shard only by chance, about one time in 11 (6 of 48 across the
    runs), and then removes the entry.
  - A listing runs the sweep for an entry with a pending op or one that a
    force-check filter selects (`rgw_rados.cc:9820-9834`). Three triggers
    reached it and left entries behind: `radosgw-admin bucket list` on a
    head with a pending op; S3 ListObjectsV2, where radosgw itself runs it;
    and `radosgw-admin bucket check --check-objects --fix`, but only while
    the upload's `.meta` entry is still indexed.
  - Without `--fix`, `bucket check --check-objects` never reaches the sweep:
    `check_object_index` returns -EINVAL before it lists anything
    (`rgw_bucket.cc:431-434`), and radosgw-admin discards the error
    (`rgw_admin.cc:8742`).
  - With `--fix` and no `.meta` entry, `check_bad_index_multipart` runs
    first (`rgw_bucket.cc:1260`) and removes every part entry of the upload
    through `remove_objs_from_index`, which hashes each part by its upload's
    key (`rgw_rados.cc:10278`). The sweep's deletes then find nothing, which
    hides the defect.
- **Releases:** every release since bucket index sharding added the hash
  source (8a04c0a61bc, first in v0.92); the sweep itself dates from
  e5dc46f6aa9 (2012) and has only been refactored since. Checked at v19.2.6,
  v20.2.4 and main.
- **rgw-go:** unit W's listing reconciliation reproduces radosgw's bytes,
  wrong shard included, so the index a shared zone sees is the one radosgw
  would leave.
- **Upstream:** [#81121](https://tracker.ceph.com/issues/81121), filed after a
  full-text tracker and all-time pull-request search (2026-09-29) found no
  report or fix. The same wrong-shard mistake with multipart entries was
  fixed at other call sites: resharding
  ([#43583](https://tracker.ceph.com/issues/43583),
  [ceph/ceph#32617](https://github.com/ceph/ceph/pull/32617)), `bi put`
  ([#53248](https://tracker.ceph.com/issues/53248),
  [ceph/ceph#43908](https://github.com/ceph/ceph/pull/43908)) and `bucket
  check --fix` ([#53874](https://tracker.ceph.com/issues/53874),
  [ceph/ceph#46030](https://github.com/ceph/ceph/pull/46030)). The last is
  the bug that journals of [#16767](https://tracker.ceph.com/issues/16767)
  and [#44660](https://tracker.ceph.com/issues/44660) describe, where `bucket
  check --check-objects --fix` removed leftover part entries only on
  unsharded buckets. `bucket check --fix` removes the part entries of an
  upload with no `.meta` entry through `remove_objs_from_index`, which until
  the fix, 0521c2ae830, sent every removal to `.dir.<bucket_id>`, the index
  object only an unsharded bucket has (`rgw_rados.cc:9111` and `:9132`,
  `svc_bi_rados.cc:97-119` at 0521c2ae830^). The fix is in v18.0.0 and
  later, and on quincy from v17.2.8. #53874's pull request field names
  [ceph/ceph#44580](https://github.com/ceph/ceph/pull/44580), an earlier
  version of the fix that was closed unmerged. #16767 and #44660 were closed by
  [ceph/ceph#49709](https://github.com/ceph/ceph/pull/49709), which fixed
  another cause.
- **Found:** phase 1 plan review, 2026-09-28; verified 2026-09-29;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.

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
- **rgw-go:** meets it as radosgw does until a release carries the class
  fix: the class decides, so no client can work around it. By the owner's
  decision, phase 1's cancel (unit W) sends radosgw's -1:0 version, so
  rgw-go meets the explicit-cancel case too.
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
  - Reproduced on v19.2.6 and v20.2.4: an index entry written with
    `radosgw-admin bi put` and `ver.epoch` 65535 or 65537 reads back intact
    through `bi list`, and one written with 65536 reads back as epoch 0.
    Each release's ceph-dencoder decodes the two-byte encoding of 65536 as
    epoch 0 and a four-byte encoding as 65536. It is the only affected value:
    the two-byte branch takes 0x100 through 0x10000, and only 0x10000 does
    not fit in 16 bits.
- **Releases:** every release.
- **rgw-go:** reproduces it for byte identity. `encodePacked` in
  `internal/cls/rgw/types_key.go` truncates 0x10000 to 0, and
  `fixtures_test.go` pins the bytes.
- **Upstream:** [#80995](https://tracker.ceph.com/issues/80995); its
  confirmation note carries the live output from both releases and a C++
  reproducer, `repro_packed_val.cc`.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-009); verified 2026-09-27;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.

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
  A negative value is accepted too. A negative `rgw_lc_max_objs` or
  `rgw_gc_max_objs` fails earlier, at startup: `RGWLC::initialize` and
  `RGWGC::initialize` allocate an array of that many shard names
  (`rgw_lc.cc:237-241` and `driver/rados/rgw_gc.cc:35-37` at v19.2.6 and
  v20.2.4), which with GCC throws a plain `std::bad_alloc`;
  `RGWRados::init_complete` runs both (`rgw_rados.cc:1326-1327` and, with
  GC, `:1223-1225` at v19.2.6; `:1310-1311` and `:1285-1287` at v20.2.4). A
  negative `rgw_usage_max_shards` does not fault, but `usage_log_hash`
  converts it to an unsigned divisor of 2^32 minus its magnitude, so each
  usage object is named by the hash itself (`rgw_rados.cc:1615-1628` at
  v19.2.6, `:1718-1731` at v20.2.4). The all-users usage read and trim walk
  the shard index up from 0 until the name comes back to `usage.0`, the read
  also stopping once it is truncated or has filled its entry budget
  (`:1681-1714` and `:1723-1733` at v19.2.6, `:1784-1817` and `:1826-1836`
  at v20.2.4). That divisor puts `usage.0` at least 2^31 steps away, each a
  RADOS call, and a missing shard object only moves the walk on: both skip
  ENOENT (`:1697-1698`, `:1729-1730`; v20.2.4 `:1800-1801`, `:1832-1833`),
  and the read clears `is_truncated` before its call
  (`cls/rgw/cls_rgw_client.cc:798-799` at v19.2.6, `:607-608` at v20.2.4).
  The admin API reaches both: a usage GET that names no user or bucket, and
  a usage DELETE with `remove-all`, read or trim for a user with an empty
  id (`rgw_rest_usage.cc:38-41`, `:69`, `:91-94` and `:108-120`, the same
  at both tags; `rgw_sal_rados.cc:252` and `:261` at v19.2.6, `:269` and
  `:278` at v20.2.4). This loop is from code reading, not reproduced.
- **Releases:** every release. GC faults as a SIGFPE at v19.2.2 and as an
  out-of-bounds read from v19.2.3 and v20.1.0 on; LC and usage fault as a
  SIGFPE throughout, since the `rgw_shards_mod` guard does not cover their
  direct modulo.
- **rgw-go:** config validation rgw-go must add. Phase 1 rejects or floors a
  zero `rgw_gc_max_objs` (GC worker, unit W), and a zero or negative
  `rgw_lc_max_objs` and `rgw_usage_max_shards` (metadata and lifecycle path,
  unit M), at startup rather than faulting on first use, and never divides
  by a shard count without guarding it.
- **Upstream:** [#80991](https://tracker.ceph.com/issues/80991), which we
  filed. Its fix, [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160),
  opened in response by the maintainer Matthew Heler (mheler) and not a
  draft, adds `min: 1` to the three options in `rgw.yaml.in` and is in
  review. It was verified to cover all three sites, and the option minimum
  also clamps a 0 already stored in the configuration when it is loaded.
  [#75958](https://tracker.ceph.com/issues/75958), a crash when
  rgw_gc_max_objs changes at runtime, is related but has a different trigger.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-019); verified 2026-09-27;
  not reproduced on a running cluster.

## radosgw truncates a long aws-chunked trailer section instead of rejecting it

- **Kind:** defect, unfixed through main.
- **Evidence:** at v20.2.4 `AWSv4ComplMulti::complete` reads the trailer
  section into a 256-byte buffer but caps each read at 256 - pos - 1 bytes
  (`rgw_auth_s3.cc:1573-1576`; v19.2.6 `:1596-1599`). beast answers a
  0-byte read with 0 (`rgw_asio_frontend.cc:152-175`), so the position
  stops at 255, and the size check `tbuf_pos == trailer_buf_size` never
  fires (`rgw_auth_s3.cc:1585-1590`; v19.2.6 `:1608-1613`). Its error,
  ERR_LIMIT_EXCEEDED, is 409 LimitExceeded (`rgw_common.cc:87`). A longer
  section is silently cut at 255 bytes.
  - A signed trailer (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER`) whose
    `x-amz-trailer-signature` line is cut fails with 403
    SignatureDoesNotMatch (`rgw_auth_s3.cc:1663-1667`, v19.2.6
    `:1686-1690`; `rgw_common.cc:92`). Measured on v19.2.6 and v20.2.4:
    signed sections up to 257 bytes pass, since only the closing CRLF is
    lost, and longer ones fail with 403. A signed SHA512 trailer is 290
    bytes, so every signed SHA512 upload fails, on Squid too, which ignores
    checksums but still fails the request; v20.2.4 supports SHA512
    (`rgw_cksum.h:49`). A signed SHA256 trailer is 246 bytes and passes.
  - On v20.2.4 an unsigned section (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`)
    whose checksum line falls past the cut is accepted, and the client's
    value is never compared: with no expected checksum, PutObj stores the
    one radosgw computed (`rgw_op.cc:4757-4790`).
  - Latent: the copy and the reads into the buffer write past the end of an
    empty `static_vector` (`rgw_auth_s3.cc:1569-1576`), which works only
    because its inline capacity is 256.
  - The same code is on main (`rgw_auth_s3.cc:1616` and `:1625`,
    2026-09-29).
- **Releases:** v19.1.0 and later, so every Squid and Tentacle release, and
  reef from v18.2.5; absent at v18.2.4. Checked at v18.2.5, v18.2.8,
  v19.1.0, v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1 (unit A) bounds the trailer section at 1 KiB and
  answers 409 LimitExceeded above it, since the largest legitimate section,
  a signed SHA512 trailer, is 290 bytes. That is a difference from radosgw,
  which unit A records in `docs/exclusions.md`.
- **Upstream:** [#81122](https://tracker.ceph.com/issues/81122). The trailer
  code came with [ceph/ceph#54856](https://github.com/ceph/ceph/pull/54856),
  the fix for [#63153](https://tracker.ceph.com/issues/63153), and reached
  reef through [ceph/ceph#58435](https://github.com/ceph/ceph/pull/58435).
  [ceph/ceph#64934](https://github.com/ceph/ceph/pull/64934) rewrote the
  trailer parse but kept this read loop, and was closed unmerged.
- **Found:** phase 1 planning of unit A, 2026-09-29; reproduced 2026-09-29
  on disposable Squid and Tentacle clusters.

## radosgw accepts a negative or overflowing aws-chunked chunk size

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `AWSv4ComplMulti::ChunkMeta::create_next` reads
  each chunk size with `std::strtoull(metabuf, &data_field_end, 16)` and
  rejects only a parse that consumed nothing (`rgw_auth_s3.cc:1127-1132`);
  errno is never checked. strtoull negates a leading `-` in unsigned
  arithmetic and saturates on overflow, so "-1" and any size of more than
  16 significant hex digits become 2^64-1. The chunk's end,
  `offset + data_length`, then wraps (`:1095-1108`), and the rest of the
  current read is taken as this chunk's data, the next chunk's framing
  included. The same code is at v20.2.4 (`:1104-1109`, `:1072-1085`).
  - Measured on v19.2.6 and v20.2.4, with identical results. An unsigned
    upload (`STREAMING-UNSIGNED-PAYLOAD-TRAILER`) of two 48-byte chunks
    whose first size is "-1" or `1ffffffffffffffff` is answered 200 and
    stored corrupted: chunk one, then the six framing bytes between the chunks,
    then the first 42 bytes of chunk two. A single-chunk upload is not
    corrupted, since the object is capped at `x-amz-decoded-content-length`
    (`:1534-1542`).
  - The same upload, signed (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD`), fails
    with 400 XAmzContentSHA256Mismatch: the misframed bytes are hashed as
    one chunk, so the chunk-signature check that `complete()` makes for the
    last data chunk fails (`:1557-1563`), and
    `RGWOp::do_aws4_auth_completion` reports a failed `complete()` as that
    error (`rgw_op.cc:1370-1371`; v20.2.4 `:1607-1608`). A signed
    single-chunk upload with a malformed size is accepted with the exact
    bytes.
  - On main `create_next` no longer rejects even a parse that consumed
    nothing (`rgw_auth_s3.cc:1146-1151`, 2026-09-29).
- **Releases:** every release since v12.1.0; checked at v19.2.6, v20.2.4 and
  main.
- **rgw-go:** phase 1 (unit A) accepts a chunk size only as strict hex, 1 to
  16 digits. That is a difference from radosgw, which unit A records in
  `docs/exclusions.md`.
- **Upstream:** [#81123](https://tracker.ceph.com/issues/81123). The parse
  came with def8f6412a5, in
  [ceph/ceph#14885](https://github.com/ceph/ceph/pull/14885), and
  [ceph/ceph#63326](https://github.com/ceph/ceph/pull/63326) dropped its one
  check on main. [#21003](https://tracker.ceph.com/issues/21003) and
  [#45790](https://tracker.ceph.com/issues/45790) concern the same lenient
  chunk-metadata parse with other inputs; the latter's
  [ceph/ceph#35350](https://github.com/ceph/ceph/pull/35350) was closed
  unmerged.
- **Found:** phase 1 planning of unit A, 2026-09-29; reproduced 2026-09-29
  on disposable Squid and Tentacle clusters.

## radosgw writes ACL owner and grantee names into its XML unescaped

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 the S3 ACL writer puts the owner's ID and display
  name between their tags as they are (`rgw_acl_s3.cc:177`, `:179`), and a
  canonical-user grantee's the same way (`:260`, `:262`). GetBucketAcl and
  GetObjectAcl send that text as their document after the XML declaration
  (`dump_start`, `rgw_rest.cc:571-577`): `RGWGetACLs::execute` builds it with
  `rgw::s3::write_policy_xml` (`rgw_op.cc:5725-5734`), and
  `RGWGetACLs_ObjStore_S3::send_response` writes it with `dump_body`
  (`rgw_rest_s3.cc:3605-3614`), outside the XML formatter, which escapes
  the text of radosgw's other documents (`xml_stream_escaper`,
  `escape.cc:134-169`).
  - A display name holding markup therefore yields malformed XML, as `A&B`
    does, or well-formed XML that a client reads as another name or with
    extra markup, as `&amp;` (read as `&`) and `<b/>` (an empty `b`
    element) do; Python's ElementTree parses these documents so.
  - Only an administrator can set such a name. User create and modify, which
    radosgw-admin and the admin API run, copy the display name as given
    (`driver/rados/rgw_user.cc:1755`, `:2070`) and hold it to the IAM
    user-name pattern `[\w+=,.@-]+` only for an account's users (`:1840`,
    `:2198`; `rgw_rest_iam.cc:172-188`).
  - The same at v20.2.4, where rgw_acl_s3.cc and escape.cc are
    byte-identical to v19.2.6 (`rgw_rest.cc:576-582`,
    `rgw_op.cc:6305-6314`, `rgw_rest_s3.cc:3888-3897`, `rgw_user.cc:1761`,
    `:2076`, `:1846`, `:2204`, `rgw_rest_iam.cc:175-191`), and on main,
    which writes the same lines (2026-09-30).
- **Releases:** every release checked: v17.2.0, v18.2.8, v19.2.0, v19.2.6,
  v20.2.0, v20.2.4, v21.3.0 and main. The owner writer already wrote the ID
  and display name raw in 2009 (`rgw_acl.h:341-346` at f8fb331226c).
- **rgw-go:** the ACL documents (unit Z) escape the owner's and each
  grantee's ID and display name with `xmltext.Escape`, the escaping
  radosgw's formatter gives the text of its other documents (owner decision
  12c). `docs/exclusions.md` records the difference.
- **Upstream:** no tracker issue or pull request reports or fixes it
  (searched 2026-09-29). Each search was first run on a known match.
  - tracker.ceph.com, full text: every project's issues of every status
    through the issue filter "any searchable field contains", which covers
    subjects, descriptions and notes. It finds
    [#80948](https://tracker.ceph.com/issues/80948) by
    `oath_totp_validate4_callback`, a word only in its description, and
    [#73564](https://tracker.ceph.com/issues/73564) by a phrase only in a
    note; the site's search misses the first. Terms: `rgw_acl_s3`,
    `write_policy_xml`, `xml_stream_escaper`, `escape_xml`,
    `ACLOwner to_xml`, `ACLGrant to_xml`, `GetBucketAcl`, `GetObjectAcl`,
    `acl escape`, `acl escaping`, `DisplayName escape`,
    `display name escape`, `acl ampersand`, `acl xml invalid`,
    `acl xml malformed`, `acl not well-formed`, `unescaped xml`,
    `xml escape`, `xml escaping`, `ampersand`, `acl special characters`,
    `display name special characters`. Also the subjects of the 1,652 rgw
    issues updated since 2026-06-01.
  - Pull requests touching `src/rgw/rgw_acl_s3.cc` or its header. Merged:
    no change in the file's history on main since its creation in 2012
    escapes these fields (`git log -G escape` finds none), and squid and
    tentacle have not touched it since the floor tags. Open and closed
    unmerged: a scan of each pull request's changed files, which flags
    [ceph/ceph#54526](https://github.com/ceph/ceph/pull/54526) among
    November 2023's merged rgw pull requests, finds the file in 2 of the
    1,562 open pull requests, which change only an include, and in 38
    of the 2,156 closed unmerged pull requests labelled rgw (the labeler
    adds that label to every pull request touching `src/rgw/` since
    2020-11-12). None of those changes escapes these fields; ten that
    touch over 2,000 files, branches merged onto the wrong base, were
    not read.
  - Pull request text (titles, bodies and comments), which finds
    [ceph/ceph#54526](https://github.com/ceph/ceph/pull/54526) by a phrase
    only in its body: `acl escape`, `acl xml escape`, `DisplayName escape`,
    `display name xml`, `rgw_acl_s3 escape`, `to_xml escape`,
    `xml_stream_escaper`, `acl ampersand`, `GetBucketAcl xml`,
    `acl invalid xml`. The one hit,
    [ceph/ceph#19845](https://github.com/ceph/ceph/pull/19845), reworked the
    formatter's escaping and does not touch the ACL writer.
  - Related: [#59077](https://tracker.ceph.com/issues/59077), open since
    2023-03, proposes refusing user IDs and display names that hold REST or
    IAM-policy delimiters; it does not mention the ACL documents.
  - Not filed: the defect has not been reproduced on a running cluster.
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the
  source, not reproduced.

## radosgw ignores a payload-hash mismatch on bodies read by read_all_input

- **Kind:** defect, a regression; unfixed through main, with a fix in review.
- **Evidence:**
  - A signed single-chunk body gets an `AWSv4ComplSingle` completer (v19.2.6
    `rgw_rest_s3.cc:5903-5946`; v20.2.4 `:6470-6517`), whose `complete()`
    compares the body's SHA-256 with `x-amz-content-sha256`
    (`rgw_auth_s3.cc:1737-1756`; v20.2.4 `:1714-1733`).
  - `RGWOp::read_all_input` and `get_json_input` read the body, call
    `do_aws4_auth_completion()` and drop its result (v19.2.6
    `rgw_op.h:215-237`; v20.2.4 `:229-251`). `do_aws4_auth_completion` moves
    the completer out of the request before it checks (v19.2.6
    `rgw_op.cc:1367`; v20.2.4 `:1604`), so any later call returns 0.
  - CreateBucket, PutBucketAcl and PutObjectAcl, PutBucketPolicy,
    PutObjectTagging and PutBucketTagging, CompleteMultipartUpload and
    DeleteObjects read their body through `read_all_input` (v19.2.6
    `rgw_rest_s3.cc:2491`, `rgw_rest.cc:1474`, `rgw_op.cc:8078`,
    `rgw_rest_s3.cc:788`, `:880`, `rgw_rest.cc:1590`, `:1672`; v20.2.4
    `rgw_rest_s3.cc:2611`, `rgw_rest.cc:1479`, `rgw_op.cc:9004`,
    `rgw_rest_s3.cc:870`, `:962`, `rgw_rest.cc:1595`, `:1677`). The checks
    that the ACL PUTs, CompleteMultipartUpload and DeleteObjects make
    afterwards therefore always pass (v19.2.6 `rgw_rest_s3.cc:3618-3623`,
    `:4073`, `:4244`; v20.2.4 `:3901-3906`, `:4601`, `:4792`), and none of
    these ops compares a Content-MD5 instead. radosgw acts on a signed body
    whose hash does not match and answers as if it did; the mismatch is only
    logged, at level 10.
  - The other subresource PUTs read their body the same way: lifecycle,
    object lock, legal hold, replication, versioning, website, CORS, request
    payment, retention, public-access block and encryption (v19.2.6
    `rgw_rest.cc:1482-1496`, `rgw_op.cc:8616`, `:8747`, and six sites in
    `rgw_rest_s3.cc`). Of them only the lifecycle PUT compares a Content-MD5,
    when one is sent (`rgw_op.cc:5942-5981`).
  - PutObject and UploadPart do return the verdict (v19.2.6
    `rgw_rest_s3.cc:2706-2714`, `rgw_op.cc:4439`; v20.2.4
    `rgw_rest_s3.cc:2867-2875`, `rgw_op.cc:4671`).
  - It is a regression. 0214b4a7afb, "rgw: handle aws4 completion when
    reading all op data" (first in v17.1.0), moved these ops to the helper.
    Before it, CreateBucket, the ACL PUTs, CompleteMultipartUpload,
    DeleteObjects and the versioning, website and CORS PUTs returned the
    verdict (v16.2.15 `rgw_rest_s3.cc:2268-2271`, `:3341`, `:3721`, `:3903`,
    `:1978`, `:2052`, `:3486`).
  - The same code is on main (`rgw_op.h:237` and `:248`, 7ed73efc1be,
    2026-09-25).
- **Trigger:** a signed request whose body changes after signing. The
  signature covers the `x-amz-content-sha256` value, not the body, so anyone
  who can alter a request between the client and radosgw (plain HTTP, or
  behind a proxy that terminates TLS) can replace such a body, for example a
  bucket policy, an ACL, a DeleteObjects key list or a CompleteMultipartUpload
  part list, and radosgw acts on the replacement.
- **Releases:** every release from v17.1.0 on; checked at v16.2.15, which
  returns the verdict, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1 verifies the hash before it acts: every op that acts on
  a request body reads the body to its end first, and a mismatch is 400
  XAmzContentSHA256Mismatch with nothing changed (units M, W and P). That is
  a difference from radosgw, which those units record in `docs/exclusions.md`.
- **Upstream:** we filed [#81230](https://tracker.ceph.com/issues/81230).
  The fix in review is
  [ceph/ceph#72263](https://github.com/ceph/ceph/pull/72263) (draft): both
  helpers return the completion's result, a two-line `rgw_op.h` change that
  covers every affected op. It has not been compiled against a patched build
  yet; the pull request says so, and Ceph's CI builds it. No earlier tracker
  issue or pull request reports it (full-text tracker and all-time
  pull-request search, 2026-09-30).
  [#71607](https://tracker.ceph.com/issues/71607) and
  [ceph/ceph#64569](https://github.com/ceph/ceph/pull/64569) are related but
  distinct: they changed only the single-object DeleteObj `get_params` path.
  The helper came with
  [ceph/ceph#39678](https://github.com/ceph/ceph/pull/39678).
- **Found:** phase 1 planning of unit A, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on v19.2.6 and v20.2.4 on
  2026-09-30. PutBucketTagging, PutBucketPolicy and CreateBucket accept a
  substituted body, and the readback shows the substitute applied, while
  PutObject answers 400. With `debug_rgw=20` the completer logs the mismatch
  while the op answers 2xx. The reproducer, its output and the debug excerpt
  are attached to the tracker issue.

## radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned

- **Kind:** defect, a regression; fixed upstream, but in no squid or tentacle
  release yet.
- **Evidence:**
  - Unless `rgw_sigv4_insecure` is set, `get_v4_canonical_headers` refuses a
    request that carries a Content-Type header its SignedHeaders do not list,
    logging "Signature rejected: 'content-type' supplied but not in
    CanonicalHeaders" (`rgw_auth_s3.cc:788-802` at v19.2.6, `:765-779` at
    v20.2.4). The same block requires `host` and every `x-amz-*` header to be
    signed. So a presigned PUT URL signed over `host` alone fails once the
    client that uses it sends a Content-Type.
  - The check came with the CVE-2026-54330 hardening, "rgw: Follow
    guidelines when verifying SigV4 signatures" (d8ba9d0cb06 on squid and
    683fad73231 on tentacle, both 2026-06-16); v19.2.5 and v20.2.3 do not
    have it.
  - The fix, 534308306f2, removes the Content-Type requirement and keeps the
    `host` and `x-amz-*` checks.
- **Releases:** v19.2.6 and v20.2.4. The fix is on main (c3179de982f,
  2026-08-21), the squid branch (18d35e32770, 2026-08-26) and the tentacle
  branch (7e2e2072822, 2026-09-03), but neither branch has had a release
  since (checked 2026-09-30). The first tag that carries it is v21.1.1
  (2026-09-10), an umbrella release candidate; no umbrella tag ever carried
  the check.
- **rgw-go:** not decided yet: its SigV4 signature check is not written.
  `auth.Config.Insecure`, which reads `rgw_sigv4_insecure`, lists the
  Content-Type check among those the option skips.
- **Upstream:** [#79674](https://tracker.ceph.com/issues/79674), resolved,
  with its duplicate [#79708](https://tracker.ceph.com/issues/79708) and its
  backports [#79723](https://tracker.ceph.com/issues/79723) (squid),
  [#79725](https://tracker.ceph.com/issues/79725) (tentacle) and
  [#79724](https://tracker.ceph.com/issues/79724) (umbrella). The fix is
  [ceph/ceph#71192](https://github.com/ceph/ceph/pull/71192), backported by
  [ceph/ceph#71296](https://github.com/ceph/ceph/pull/71296) (squid),
  [ceph/ceph#71364](https://github.com/ceph/ceph/pull/71364) (tentacle) and
  [ceph/ceph#71363](https://github.com/ceph/ceph/pull/71363) (umbrella).
- **Found:** phase 1 authentication work (unit A), 2026-09-30, in the
  prior-art search for url_decode; verified in the source at both tags. Not
  reproduced by rgw-go; the reporter of #79674 met it on a v20.2.4 cluster.

## radosgw checks a copy source with inputs from the destination bucket

- **Kind:** defect, unfixed through main, with a fix in review for the owner
  and tenant inputs.
- **Evidence:** CopyObject and UploadPartCopy authorize their source inside
  `verify_permission` through the request's `req_state`, whose bucket is the
  destination (v19.2.6 `rgw_op.cc:5420-5422` and `:5454-5459`, `:3933-3934`
  and `:3956-3961`; v20.2.4 `:5986-5988`, `:6020-6025`, `:4142-4143` and
  `:4165-4170`). The source's ACLs and stored policy are read, but three
  inputs are the destination's:
  - Owner: for an identity that belongs to an account, the cross-account
    decision compares the account with `s->bucket_owner` (v19.2.6
    `rgw_common.cc:1386-1403`; v20.2.4 `:1414-1433`), the owner of the
    destination's ACL (`rgw_op.cc:552`; v20.2.4 `:582`). An account user
    copying from another account's bucket into their own account's bucket
    takes the same-account path, which consults no ACL and allows on an
    Allow from an identity policy, or for the account root (v19.2.6
    `rgw_common.cc:1236-1244`; v20.2.4 `:1249-1257`). The source's owner
    need grant nothing, where a GET of the same object needs its grant.
    CopyObject decides on the bucket owner alone. UploadPartCopy decides on
    the owner of the source object's ACL and falls back to `s->bucket_owner`
    (v19.2.6 `rgw_common.cc:1533-1534`; v20.2.4 `:1585-1586`), and a source
    object with no ACL attr gets a default ACL owned by `s->bucket_owner`
    (v19.2.6 `rgw_op.cc:421-422`, `:307-310`; v20.2.4 `:451-452`,
    `:350-353`), so UploadPartCopy is affected only for a source object
    whose ACL is missing or names no owner.
  - Tenant: the source bucket's policy is parsed with `s->bucket_tenant`,
    the destination's tenant (v19.2.6 `rgw_op.cc:419`; v20.2.4 `:449`). The
    parser binds a Resource whose account is empty or `*` to that tenant
    (`rgw_iam_policy.cc:690-696`; v20.2.4 `:703-709`), while the source
    object's ARN carries the source's tenant, which a match compares
    (`rgw_arn.cc:126-130` and `:335-337` at both tags). In a cross-tenant
    copy the policy's Resource elements never match the source object and
    its NotResource elements never exclude it, so a Deny the owner scoped
    with Resource does not stop a copy that the source's ACL allows. A
    Resource that names the source's tenant fails the parse
    (`rgw_iam_policy.cc:697-701`; v20.2.4 `:710-714`); see the next entry.
  - Public-access block: the permission state takes the bucket and its
    public-access block from the request (v19.2.6 `rgw_common.cc:1099-1109`,
    `rgw_op.cc:585`; v20.2.4 `rgw_common.cc:1112-1122`, `rgw_op.cc:615`).
    The source's ACLs are evaluated with the destination's IgnorePublicAcls
    (v19.2.6 `rgw_common.cc:1420-1423`, `:1572-1575`; v20.2.4 `:1453-1454`,
    `:1628-1629`), and v20.2.4 tests RestrictPublicBuckets with the
    destination's block and owner against the source's policy (`:1376-1377`,
    `:1543-1544`); v19.2.6 does not evaluate RestrictPublicBuckets. A public
    ACL grant, or on v20.2.4 a public policy, on a source whose owner
    blocked it lets a requester copy the object into a bucket without the
    block, where a GET is refused.
  - v20.2.4's source check also compares `x-amz-expected-bucket-owner` with
    the destination's owner (`rgw_common.cc:1407-1412`, `:1575-1580`). That
    header names the destination bucket, so this comparison is correct.
  - The owner and tenant inputs are the same on main (`rgw_common.cc:1430`,
    `:1607`, `rgw_op.cc:524-526`, 7ed73efc1be, 2026-09-25).
- **Trigger:** a requester who may write to the destination: an account user
  whose account owns it, for the owner input; a requester whose destination
  is in another tenant than the source, for the tenant input; and a
  destination without the source's public-access block, for the last.
- **Releases:** every Squid and Tentacle release; checked at v19.2.6, v20.2.4
  and main. The owner input came with IAM accounts in f917e999c2c, "rgw: add
  cross-account policy evaluation" (first in v19.1.0), and the tenant input
  in v19.2.0 and v20.1.0 (see Upstream); reef parsed the source's policy
  with the source's tenant (v18.2.8 `rgw_op.cc:403`).
- **rgw-go:** phase 1 (unit Z) checks a copy source against the source
  bucket: its owner decides the cross-account path, is the object-owner
  fallback and owns a default object ACL; its policy is parsed with its own
  tenant; and its public-access block applies in addition to the
  destination's. The destination supplies only requester pays and the
  `x-amz-expected-bucket-owner` comparison. That is a difference from
  radosgw, which unit Z records in `docs/exclusions.md`.
- **Upstream:** we filed [#81248](https://tracker.ceph.com/issues/81248).
  The fix in review is
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) (draft): the
  source check takes the source bucket's tenant and owner, through a new
  `verify_bucket_permission` overload that leaves the other callers
  unchanged. It does not change the public-access block input: the source
  check still applies the destination's IgnorePublicAcls and
  RestrictPublicBuckets, which matters only for a public source, and the
  issue and the pull request leave that as a follow-up. The pull request
  has not been compiled against a patched build yet, and says so. No
  earlier tracker issue or pull request reports it (full-text tracker and
  all-time pull-request search, 2026-09-30). The tenant input came with
  [ceph/ceph#59169](https://github.com/ceph/ceph/pull/59169) and its squid
  backport [ceph/ceph#59221](https://github.com/ceph/ceph/pull/59221), the
  fix for [#67464](https://tracker.ceph.com/issues/67464), which pass the
  request's tenant where reef passed the source's.
  [#61954](https://tracker.ceph.com/issues/61954) concerns the same
  UploadPartCopy check, for a source policy that was never read.
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on v19.2.6 and v20.2.4 on
  2026-09-30, cross-tenant with IAM accounts: tenant B's GET of tenant A's
  private object answers 403, while B's CopyObject of the same object into
  B's own bucket answers 200, and the copy reads back byte-identical. The
  radosgw log shows the account-root grant deciding `s3:copy_obj`. The
  reproducer, written in Go, its output and the patch are attached to the
  tracker issue.

## radosgw terminates on a copy source or system request whose bucket policy does not parse

- **Kind:** defect, unfixed through main, with a fix in review.
- **Evidence:**
  - CopyObject and UploadPartCopy parse the source bucket's stored policy
    first thing in `verify_permission`, with no handler (v19.2.6
    `rgw_op.cc:5420` and `:3933` into `read_obj_policy`, `:419`; v20.2.4
    `:5986`, `:4142`, `:449`). The parse constructs a `Policy` (v19.2.6
    `rgw_op.cc:328-338`; v20.2.4 `rgw_common.cc:3275-3285`), which throws
    `PolicyParseException` when it fails (`rgw_iam_policy.cc:1815-1824`;
    v20.2.4 `:1850-1859`). The same parse of the request's own bucket
    policy has a handler, which answers 403 (v19.2.6 `rgw_op.cc:598-615`;
    v20.2.4 `:628-645`).
  - Nothing on the request path catches the exception. `process_request`
    catches only `DigestException` (v19.2.6 `rgw_process.cc:343` and
    `:410`; v20.2.4 `:345` and `:417`), and the completion handler of the
    beast connection coroutine rethrows it (v19.2.6
    `rgw_asio_frontend.cc:1203-1205`, `:1220-1222`; v20.2.4 `:1116-1117`,
    `:1133-1134`). The frontend runs on radosgw's io_context pool (v19.2.6
    `rgw_appmain.cc:474`; v20.2.4 `:482`), whose threads call `ioctx.run()`
    with no handler either (v19.2.6 `common/async/context_pool.h:81-85`;
    v20.2.4 `:80-84`; `common/Thread.h:72-82` at both tags), so the
    exception leaves the thread and `std::terminate` ends the process.
  - A stored policy fails the parse when the parser has changed since it was
    stored, which the handler for the request's own bucket anticipates ("a
    parsing failure here means we broke backward compatibility", v19.2.6
    `rgw_op.cc:603-606`). From v19.2.0 a policy that parses for its own
    bucket fails too: the source's policy is parsed with the destination's
    tenant (previous entry), and a Resource naming the source's tenant,
    which the source's PutBucketPolicy accepts (v19.2.6
    `rgw_op.cc:8099-8101`; v20.2.4 `:9025-9027`), fails for any other
    tenant (`rgw_iam_policy.cc:697-701`; v20.2.4 `:710-714`).
  - A system request, one authenticated as a system user as multisite sync
    is (`rgw_auth_filters.h:320-330`; v20.2.4 `:348-356`), meets the same
    unhandled parse on its own bucket. The handler in `init_permissions`
    refuses only other requests (`rgw_op.cc:612-614`; v20.2.4 `:642-644`),
    so a system request goes on to `read_permissions`, whose
    `rgw_build_object_policies` calls `read_obj_policy` on the same bucket
    (`rgw_op.cc:8003-8009`, `:646-648`; v20.2.4 `:8921-8927`, `:676-678`),
    and that parses the policy again with no handler (`:419`; v20.2.4
    `:449`).
  - CopyObject's `verify_permission` parses the destination bucket's policy
    once more, also with no handler (v19.2.6 `rgw_op.cc:5475`; v20.2.4
    `:6041`; main `:6394` at 7ed73efc1be). `init_permissions` has already
    refused any other request whose own bucket policy does not parse, so
    only a system request reaches this parse with such a policy.
  - The same code is on main (`rgw_op.cc:524`, `rgw_process.cc:461`,
    `rgw_asio_frontend.cc:1204` and `:1221`, 7ed73efc1be, 2026-09-25).
- **Trigger:** a CopyObject or UploadPartCopy that names such a source and
  reaches `verify_permission`. The parse comes before the copy's permission
  checks, so the requester needs no grant on either bucket; for a source
  whose policy names its own tenant, any such request whose destination
  bucket is in another tenant ends the process. Also an object request by
  a system user on a bucket whose own stored policy does not parse.
- **Releases:** every release checked, v19.2.6, v20.2.4 and main; the
  cross-tenant case from v19.2.0 and v20.1.0.
- **rgw-go:** phase 1 (unit Z) refuses a copy whose source bucket policy does
  not parse with 403 AccessDenied, for every identity with no admin bypass,
  and parses the source's policy with the source's own tenant, so a policy
  that parses for its own bucket parses for a copy too. For the request's
  own bucket it refuses such a policy with 403 unless the requester is an
  admin, and does not parse it again. That is a difference from radosgw,
  which unit Z records in `docs/exclusions.md`.
- **Upstream:** we filed [#81253](https://tracker.ceph.com/issues/81253).
  The fix in review is
  [ceph/ceph#72271](https://github.com/ceph/ceph/pull/72271) (draft): it
  wraps both copy-path parses, the source's in `read_obj_policy` and the
  destination's in CopyObject's `verify_permission`, in the handler the
  request's own bucket already has. An ordinary request is refused with
  403, and an admin or system request continues so the bucket can be
  repaired. It touches the same `read_obj_policy` statement as
  [ceph/ceph#72270](https://github.com/ceph/ceph/pull/72270) (previous
  entry), so whichever merges second rebases. The pull request has not been
  compiled against a patched build yet, and says so. No earlier tracker
  issue or pull request reports it (full-text tracker and all-time
  pull-request search, 2026-09-30). The cross-tenant case came with
  [ceph/ceph#59169](https://github.com/ceph/ceph/pull/59169) and
  [ceph/ceph#59221](https://github.com/ceph/ceph/pull/59221) (previous
  entry).
- **Found:** phase 1 planning of unit Z, 2026-09-29; derived from the source
  and verified 2026-09-30. Reproduced live on a stock v19.2.6 on
  2026-09-30. A cross-tenant CopyObject, whose source policy names its own
  tenant in a Resource, aborted radosgw with SIGABRT (exit 134), and its
  container restarted each time. The log reaches `s3:copy_obj verifying op
  permissions` and ends in `std::terminate`, through the asio coroutine's
  rethrow, before any permission decision. `ceph crash ls` stays empty,
  because that path posts nothing to the crash module. The steps and the
  log are attached to the tracker issue. Not run live: v20.2.4, and the
  system-request path.

## radosgw checks a CopyObject source against its bucket's ACL, not the object's

- **Kind:** defect, a regression; unfixed through main.
- **Evidence:**
  - CopyObject reads the source object's ACL into `src_acl` and never uses
    it (v19.2.6 `rgw_op.cc:5409`, `:5420-5422`; v20.2.4 `:5975`,
    `:5986-5988`). It authorizes `s3:GetObject` with
    `verify_bucket_permission`, passing the source bucket's ACL
    (`:5437-5459`; v20.2.4 `:6003-6025`), and when no policy decides, that
    ACL is checked for READ, the permission `s3:GetObject` maps to
    (`rgw_common.cc:1412-1432`, `rgw_iam_policy.h:237-252`; v20.2.4
    `rgw_common.cc:1442-1469`, `rgw_iam_policy.h:247-265`).
  - So a READ grant on the source bucket, which for S3 means listing it,
    lets its grantee copy any object in it into a bucket they may write,
    including an object whose own ACL grants them nothing and whose GET is
    refused; and a READ grant on an object alone no longer lets its grantee
    copy it. UploadPartCopy still checks the object's ACL
    (`verify_object_permission` with `cs_acl`, `rgw_op.cc:3956-3961`;
    v20.2.4 `:4165-4170`).
  - It is a regression. At v18.2.8 CopyObject checked `src_acl` for READ
    (`rgw_op.cc:5415-5428`). 897d4063871, "rgw/auth: object ops use new
    verify_bucket_permission() overload" (first in v19.1.0), replaced that
    check with the bucket form.
  - The same code is on main (`rgw_op.cc:6328`, `:6340` and `:6374`,
    7ed73efc1be, 2026-09-25).
- **Releases:** every Squid and Tentacle release; checked at v18.2.8, which
  checks the object's ACL, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1's plan mirrors it: W's CopyObject checks its source
  with `VerifyBucketPermissionIn`, the bucket form. Not yet ruled; if rgw-go
  checks the object's ACL instead, `docs/exclusions.md` records the
  difference.
- **Upstream:** no tracker issue or pull request reports it (full-text
  tracker and all-time pull-request search, 2026-09-30). The bucket form
  reached squid through
  [ceph/ceph#56863](https://github.com/ceph/ceph/pull/56863). Not filed: the
  defect has not been reproduced on a running cluster, and filing needs a
  live reproduction and a C++ reproducer.
- **Found:** reading the copy-source checks for the previous entries,
  2026-09-30; derived from the source, not reproduced.

## Squid accepts RestrictPublicBuckets but never enforces it

- **Kind:** defect, fixed in Tentacle.
- **Evidence:**
  - A bucket's public-access block carries RestrictPublicBuckets
    (`rgw_public_access.h:41`), but at v19.2.6 nothing in `src/rgw` reads it
    except the block's printer (`rgw_public_access.cc:32`). A bucket whose
    owner sets it keeps granting its public bucket policy to every
    requester.
  - 07ad231606d, "rgw: implement RestrictPublicBuckets from
    PublicAccessBlock" (first in v20.1.0), makes the bucket and object checks
    refuse a requester outside the bucket owner's account when the bucket
    policy is public (v20.2.4 `rgw_common.cc:1374-1380`, `:1541-1547`).
- **Releases:** every release before v20.1.0, checked at v19.2.6; fixed from
  v20.1.0, checked at v20.2.4.
- **rgw-go:** phase 1 (unit Z) enforces it on both releases, as v20.2.4's
  radosgw does. That is a difference from Squid's radosgw, which unit Z
  records in `docs/exclusions.md`.
- **Upstream:** [#65741](https://tracker.ceph.com/issues/65741), fixed by
  [ceph/ceph#57206](https://github.com/ceph/ceph/pull/57206). Its squid
  backport, [#70860](https://tracker.ceph.com/issues/70860), is New with no
  pull request, and the reef one,
  [#70859](https://tracker.ceph.com/issues/70859), was rejected (tracker and
  pull-request search, 2026-09-30).
- **Found:** reading the copy-source checks for the copy-source entries
  above, 2026-09-30; derived from the source.

## radosgw answers 200 to a CreateBucket that loses a race to another owner

- **Kind:** defect, a regression; unfixed through main, with a fix in review
  upstream.
- **Evidence:**
  - `RGWCreateBucket::execute` reads the bucket first (v19.2.6
    `rgw_op.cc:3541-3546`; v20.2.4 `:3769-3774`). A bucket another owner
    already holds is refused there with 409 BucketAlreadyExists, since its
    ACL, owner included, differs from the request's (v19.2.6
    `rgw_op.cc:3569-3578`, `rgw_acl.cc:62-64`, `rgw_common.cc:113`; v20.2.4
    `rgw_op.cc:3797-3806`, `rgw_common.cc:114`).
  - When the name is free at that read and another owner's create lands
    before this request's own (`rgw_op.cc:3640`; v20.2.4 `:3868`),
    `RGWRados::create_bucket` returns EEXIST with the existing bucket's info
    (`rgw_rados.cc:2422-2450`; v20.2.4 `:2530-2558`), and
    `RadosBucket::create` returns `-ERR_BUCKET_EXISTS` for another owner, as
    it does for the same owner (`rgw_sal_rados.cc:182-183`; v20.2.4
    `:192-193`). `execute` continues past that code (`rgw_op.cc:3646-3647`;
    v20.2.4 `:3874-3875`). Its ownership re-check runs only for a bucket
    that existed at the read, and only when metadata is uploaded, which S3's
    CreateBucket never does (`rgw_op.cc:3649-3665`, `rgw_op.h:1121`; v20.2.4
    `rgw_op.cc:3877-3893`, `rgw_op.h:1189`). `send_response` turns
    `-ERR_BUCKET_EXISTS` into 200 (`rgw_rest_s3.cc:2544-2547`; v20.2.4
    `:2705-2708`).
  - The client is told its create succeeded, for a bucket that another owner
    holds and that is not linked to the client. What it can then do in that
    bucket is what the other owner's ACL and policy allow.
  - It is a regression. At v18.2.8 `RadosUser::create_bucket` returned EEXIST
    for another owner (`rgw_sal_rados.cc:276-277`), which CreateBucket
    answered with 409 (`rgw_rest_s3.cc:2545-2548`, `rgw_common.cc:109`).
    e2eb66a3617, "rgw/sal: move User::create_bucket() to Bucket::create()"
    (first in v19.1.0), changed that return to `-ERR_BUCKET_EXISTS`.
  - The same code is on main (`rgw_sal_rados.cc:219-220`,
    `rgw_rest_s3.cc:2822-2826`, 7ed73efc1be, 2026-09-25).
- **Releases:** every Squid and Tentacle release; checked at v18.2.8, which
  answers 409, and at v19.2.6, v20.2.4 and main.
- **rgw-go:** phase 1 (unit M) answers 409 BucketAlreadyExists whenever
  another owner holds the name, whether it is found at the read or at the
  create. That is a difference from radosgw, which unit M records in
  `docs/exclusions.md`.
- **Upstream:** [#76398](https://tracker.ceph.com/issues/76398) (Fix Under
  Review), on CreateBucket's error codes for an existing bucket, and its open
  fix [ceph/ceph#68722](https://github.com/ceph/ceph/pull/68722) were both
  opened on 2026-05-01, before our sessions. Neither mentions the race, but
  #68722 covers it: `RadosBucket::create` returns `-EEXIST` for another
  owner, which CreateBucket answers with 409 BucketAlreadyExists, and its new
  handling of `-ERR_BUCKET_EXISTS` applies only to the same owner, whatever
  `rgw_bucket_eexist_override` says. Until it merges, radosgw answers 200 by
  default on v19.2.6, v20.2.4 and main. We file nothing, neither a duplicate
  issue nor a competing pull request. The return came with e2eb66a3617 in
  [ceph/ceph#50599](https://github.com/ceph/ceph/pull/50599).
  `rgw_bucket_eexist_override`
  ([#70369](https://tracker.ceph.com/issues/70369),
  [ceph/ceph#62186](https://github.com/ceph/ceph/pull/62186), in v21.0.0 and
  later, off by default) answers 409 to every `-ERR_BUCKET_EXISTS`, the race
  included.
- **Found:** phase 1 planning of unit M, 2026-09-29; derived from the source
  and verified 2026-09-30. The rgw-bug-reproduction session confirmed the
  mechanism at v19.2.6, v20.2.4 and main, and checked that #68722 changes
  the race's path; it was not reproduced on a running cluster.

## radosgw's admin API bypass-gc removal leaks a tail and runs unbounded

- **Kind:** defect, unfixed through main.
- **Evidence:** at v19.2.6 `RGWOp_Bucket_Remove::execute` passes `bypass-gc`
  to `RGWBucketAdminOp::remove_bucket` without calling `set_max_aio`
  (`rgw_rest_bucket.cc:225-248`), so `max_aio` keeps its default of 0
  (`rgw_bucket.h:248`) and reaches `RadosBucket::remove_bypass_gc` unclamped
  (`rgw_bucket.cc:1303-1304`); the index checks, which read the same value,
  clamp it with `std::max(1, ...)` (`:594`, `:804`). The purge starts its
  budget at that 0 (`rgw_sal_rados.cc:505`) and tests `max_aio--` before each
  location of an object's manifest (`:542`).
  - On the first object whose manifest it walks, the test reads 0, so none of
    that object's tail stripes is released, yet its head and index entry are
    removed (`:565-566`; `delete_obj_aio`, `rgw_rados.cc:10687-10732`) and
    nothing is sent to GC. The stripes are left with neither an index entry
    nor a GC entry, where only an orphan scan finds them.
  - The budget then stays negative, so the drains at `:543-550` and `:573-580`
    never run: every later tail and head delete is issued without waiting
    until the final drain (`:585`), and nothing bounds how many are in flight.
  - `radosgw-admin bucket rm --bypass-gc` sets the budget from
    `--max-concurrent-ios`, 32 by default (`rgw_admin.cc:3519`, `:6619`), and
    is not affected unless given 0.
  - The same holds at v20.2.4 (`rgw_rest_bucket.cc:225-248`,
    `rgw_bucket.h:240`, `rgw_sal_rados.cc:526` and `:563-571`) and on main
    (`rgw_sal_rados.cc:553` and `:590-598`, 2026-09-29). On main, a9b9a52ee02
    (#80213, in no release yet) also forwards `bypass-gc` to the metadata
    master, whose admin op then purges that zone's copy the same way, so a
    removal started on a secondary zone meets the defect even from
    radosgw-admin.
- **Releases:** v19.2.3 through v19.2.6, and v20.1.0 and later, through
  v20.2.4 and main. The admin op gained `bypass-gc` with 9ae2d8c4e95
  (ceph/ceph#60227, first in v20.1.0) and its squid backport e2a2aba0883
  (ceph/ceph#62994, first in v19.2.3); before them it ignored the parameter
  (v19.2.2 `rgw_rest_bucket.cc:246`). The loop dates from b7a69fca248
  (v11.0.0); before the admin op reached it, only radosgw-admin did, with its
  non-zero budget.
- **rgw-go:** phase 1's admin API (unit N) honours `bypass-gc` with its own
  bounded deletion: it releases every object's tail stripes with at most
  `rgw_gc_max_concurrent_io` deletes in flight, counts a missing stripe as
  deleted and sends the tail deletes with `pool_full_try`, as the GC worker
  does (`rgw_gc.cc:371`, `:413-415`, `:677`). N's purge task records the
  difference in `docs/exclusions.md`.
- **Upstream:** no tracker issue or pull request reports or fixes it
  (full-text tracker and all-time pull-request search, 2026-09-29). The admin
  op reached the loop through
  [ceph/ceph#60227](https://github.com/ceph/ceph/pull/60227) and its squid
  backport [ceph/ceph#62994](https://github.com/ceph/ceph/pull/62994); the
  forwarding on main came with
  [ceph/ceph#71016](https://github.com/ceph/ceph/pull/71016), the fix for
  [#80213](https://tracker.ceph.com/issues/80213). Not filed: the defect has
  not been reproduced on a running cluster, and filing needs a live
  reproduction and a C++ reproducer.
- **Found:** phase 1 planning of unit N, 2026-09-29; derived from the source,
  not reproduced.

## radosgw's bypass-gc bucket removal fails once a tail stripe is gone

- **Kind:** defect, a regression; unfixed through main.
- **Evidence:**
  - At v19.2.6 `remove_bypass_gc` releases each tail stripe with
    `cls_refcount_put` (`delete_tail_obj_aio`, `rgw_rados.cc:10659-10685`). A
    stripe that no longer exists fails the put with ENOENT, since
    `read_refcount` passes on every getxattr error but ENODATA
    (`cls_refcount.cc:26-34`, `:100-103`) and the OSD's getxattr answers
    ENOENT for a missing object (`BlueStore.cc:13247-13251`). `drain_aio`
    waits for every pending delete and returns the failure
    (`rgw_sal_rados.cc:98-112`), and the removal returns it at that drain
    (`:543-548`, `:573-578` or `:585-589`), before it removes the bucket. The
    GC worker counts the same ENOENT as done (`rgw_gc.cc:413-415`). The same
    at v20.2.4 (`rgw_rados.cc:11596-11622`, `rgw_sal_rados.cc:107-121`,
    `rgw_gc.cc:428-430`) and on main.
  - radosgw-admin drains after every 32 stripes by default, so the failure can
    stop it partway through an object, whose head and index entry stay while
    some of its stripes are gone; a retry walks that object again and can fail
    the same way, as #40587 describes for a rerun after an interrupted
    removal. radosgw-admin discards the removal's result
    (`rgw_admin.cc:8768-8779`) and exits 0 after logging the drain error.
  - Through the admin API, whose zero budget leaves only the final drain
    (previous entry), all the removal's deletes have been issued when the
    failure surfaces. The op answers 404 NoSuchBucket
    (`rgw_rest_bucket.cc:245-247`, `rgw_common.cc:98`) for a bucket that still
    exists, after removing every head whose manifest it walked, and a retry
    removes it: the purge skips a listed entry whose head is gone
    (`rgw_sal_rados.cc:522-525`), and `remove` skips a missing object
    (`:385-392`).
  - Stripes go missing when an earlier bypass-gc removal stopped partway, and,
    before 1fba459071d (#73348; v19.2.4, v20.2.1 and v21.0.0), when such a
    removal deleted stripes that a server-side copy in another bucket still
    shared. That commit replaced `cls_rgw_remove_obj`, which answers ENOENT
    for a missing object too (v19.2.2 `cls_rgw.cc:2432-2433`,
    `PrimaryLogPG.cc:8206-8207`).
  - It is a regression. The fix for #40587, bcdd7e63416 (ceph/ceph#28789),
    made the three drains ignore ENOENT on main on 2019-08-01
    (`rgw_bucket.cc:839`, `:868` and `:878` at a3039beaba8^1), but merging
    ceph/ceph#29118 on 2019-08-13 (a3039beaba8) took that branch's drains,
    which predate the fix (`rgw_bucket.cc:506`, `:535` and `:545`), so no
    release from v15.1.0 on has it. Its backports reached luminous in
    v12.2.13, mimic in v13.2.7 and nautilus in v14.2.5.
- **Releases:** every release from v15.1.0 on, checked at v15.1.0, v15.2.0,
  v15.2.17, v16.2.15, v17.2.9, v18.2.8, v19.2.6, v20.2.4 and main; before the
  fix, every release from v11.0.0 through luminous v12.2.12, mimic v13.2.6 and
  nautilus v14.2.4.
- **rgw-go:** phase 1's purge (unit N) counts a missing tail stripe as
  deleted, as the GC worker does, so a removal that meets one completes. N's
  purge task records the difference in `docs/exclusions.md`.
- **Upstream:** [#24789](https://tracker.ceph.com/issues/24789), open since
  2018 with no pull request. [#40587](https://tracker.ceph.com/issues/40587)
  reported it again in 2019 and is marked resolved by
  [ceph/ceph#28789](https://github.com/ceph/ceph/pull/28789), whose change the
  merge of [ceph/ceph#29118](https://github.com/ceph/ceph/pull/29118) lost;
  its backports, [ceph/ceph#30198](https://github.com/ceph/ceph/pull/30198)
  (luminous), [ceph/ceph#29984](https://github.com/ceph/ceph/pull/29984)
  (mimic) and [ceph/ceph#29956](https://github.com/ceph/ceph/pull/29956)
  (nautilus), kept it. No open pull request restores it (full-text tracker and
  all-time pull-request search, 2026-09-29).
  [#73348](https://tracker.ceph.com/issues/73348), fixed by
  [ceph/ceph#65772](https://github.com/ceph/ceph/pull/65772), changed how the
  loop releases stripes but not the drains. The loss is not reported: it has
  not been reproduced on a running cluster, and a report needs a live
  reproduction and a C++ reproducer.
- **Found:** phase 1 planning of unit N, 2026-09-29; derived from the source,
  not reproduced.

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
  without skipping 0 (`cls_2pc_queue.cc:186`), so the 2^32nd reservation on
  one queue, the one after id 2^32-1, gets id 0. radosgw's publisher skips
  both the commit and the abort of a reservation whose id is `NO_ID`
  (`rgw_notify.cc:1173-1174` and `:1283-1284`): the event is lost, and its
  space stays reserved until radosgw's stale-reservation cleanup, which every
  30 seconds frees reservations older than 120 seconds (`:814-815`). A
  wrapped id that collides with a live reservation answers -EAGAIN
  (`cls_2pc_queue.cc:192-196`), and nothing in radosgw retries it. The same
  holds at v20.2.4 (`cls_2pc_queue_types.h:11-12`, `cls_2pc_queue.cc:188`,
  `rgw_notify.cc:1187-1188` and `:1298-1299`) and on main
  (`cls_2pc_queue_types.h:17-18`, `cls_2pc_queue.cc:188`,
  `rgw_notify.cc:1221-1222` and `:1334-1335`).
  - Reproduced on v19.2.6 and v20.2.4 with the queue head's `last_id` written
    directly to 0xFFFFFFFF, since 2^32 real reservations are impractical: the
    next notification PUT answers 200 but its event is never queued (topic
    stats show one reservation and no new entry), the head holds reservation
    id 0 for its 4 KiB, the PUT after it gets id 1 and is queued, and the
    cleanup frees the space two to two and a half minutes later. A direct
    `cls_2pc_queue_reserve` call returns 0 with id 0.
- **Releases:** every release checked, v19.2.6 through main.
- **rgw-go:** has no notification publisher yet. Phase 3's must not use id 0
  as "no reservation"; the class commits id 0 like any other.
- **Upstream:** [#80996](https://tracker.ceph.com/issues/80996), whose
  confirmation note carries the live runs and a C++ reproducer, `repro.cc`;
  its fix, [ceph/ceph#72163](https://github.com/ceph/ceph/pull/72163), skips
  NO_ID when the id wraps, adds no test, and is in review.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-003); verified 2026-09-27;
  reproduced 2026-09-29 on disposable Squid and Tentacle clusters.
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
- **Upstream:** [#73812](https://tracker.ceph.com/issues/73812), fixed by
  [ceph/ceph#66246](https://github.com/ceph/ceph/pull/66246). Its squid
  backport, [#73893](https://tracker.ceph.com/issues/73893), is under review
  as [ceph/ceph#66345](https://github.com/ceph/ceph/pull/66345); the tentacle
  backport, [#73894](https://tracker.ceph.com/issues/73894), is resolved by
  [ceph/ceph#66491](https://github.com/ceph/ceph/pull/66491).
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-004); verified 2026-09-27.
  `docs/exclusions.md` already recorded it.

## Squid's realm reload hangs when a pubsub HTTP push has lost its wakeup

- **Kind:** defect, fixed in Tentacle, not on squid. Unreproduced: the hang
  was seen once and its cause is inferred from the code.
- **Evidence:**
  - At v19.2.6 `rgw_http_req_data::wait` checks `done` without the lock
    (`rgw_http_client.cc:74-76`). For a yield context it then stores the
    completion under the lock without checking again (`:64-71`, `:77-82`).
    `finish()` posts a stored completion and otherwise signals a condition
    variable that no coroutine waits on (`:110-116`). If `finish()` runs
    between the check and the store, the coroutine is never resumed.
  - A persistent topic's HTTP push holds a shared lock on
    `s_http_manager_mutex` across that wait (`rgw_pubsub_push.cc:94`,
    `:117-119`). A realm reload's teardown calls `rgw::notify::shutdown` near
    its end, followed only by `v1_topic_migration.stop()`
    (`rgw_rados.cc:1102-1105`). `rgw::notify::shutdown` takes the lock
    exclusively in `shutdown_http_manager` (`rgw_pubsub_push.cc:423-429`,
    called from `rgw_notify.cc:839`) before it stops the notification manager
    (`:840`). A stranded push therefore blocks the reload for ever after
    "Frontends paused" and before "driver closed" (`rgw_realm_reloader.cc:95`,
    `:101`, `:104`).
  - A failing persistent push is retried with no pause by default: the three
    `rgw_topic_persistency_*` options default to 0 (`rgw.yaml.in:3971-4003`),
    and a queue that holds entries is read again without the idle sleep
    (`rgw_notify.cc:376-383`, `:458`). An endpoint that refuses connections
    therefore drives the racy wait in a tight loop.
  - Seen once, on 2026-09-29, on a disposable Squid cluster. For about five
    minutes a radosgw ran the reproduction of
    [#80996](https://tracker.ceph.com/issues/80996), a persistent topic
    pushing to `http://127.0.0.1:1`. Two hours later it received a period
    commit, logged "Frontends paused" and never logged "driver closed"; it
    was still paused when its pod was deleted about eight minutes later.
    Every thread that `RGWRados::finalize` stops before
    `rgw::notify::shutdown` had exited, as had the AMQP and Kafka managers.
    The pubsub http_manager and notif-worker-0 threads were still alive, and
    the notification manager was still cycling. No stacks were captured.
  - At v20.2.4 `wait()` checks `done` under the lock, and `async_wait` stores
    the completion before releasing it (`rgw_http_client.cc:65-79`). This is
    Ceph commit 92dd2b9c380, "rgw/http: check 'done' under mutex", in v20.0.0
    and later. The lock path is otherwise the same (`rgw_pubsub_push.cc:97`,
    `:124`, `:432-438`).
- **Releases:** v19.1.1 (a release candidate) and v19.2.0 through v19.2.6,
  and the squid branch as of a742f50616e (2026-09-03). The unlocked check
  dates from 57887f6a364, "rgw: http client drops mutex before suspending
  coroutine", first in v15.1.0. The lock that turns it into a reload hang
  arrived with 220bd93999b, the squid fix for #65337, first in v19.1.1. Reef
  has the race but no pubsub lock (v18.2.7 `rgw_pubsub_push.cc:85-108`).
  Tentacle is not affected.
- **rgw-go:** cannot fix a coexisting radosgw. `hack/rooket/populate.sh`
  waits for the reload its period commit starts and, when it does not
  finish, stops with the command that restarts the radosgw. rgw-go has no
  notification publisher before phase 3. Phase 3's must bound every push
  with a deadline, must not hold a lock across a push that its shutdown takes
  exclusively, and must cancel outstanding pushes on shutdown.
- **Upstream:** no issue reports this stall. Not filed: filing waits on a
  reproduction on a running system and a C++ reproducer. The fix,
  [ceph/ceph#57632](https://github.com/ceph/ceph/pull/57632) (merged
  2024-06-27), cites no tracker issue and has no squid backport, open or
  merged. [#66100](https://tracker.ceph.com/issues/66100) has the same
  symptom but stalls earlier, joining the data-sync thread. Its squid
  backport, [#74738](https://tracker.ceph.com/issues/74738)
  ([ceph/ceph#67439](https://github.com/ceph/ceph/pull/67439)), is not in
  v19.2.6. The fix for [#65337](https://tracker.ceph.com/issues/65337)
  created the lock path. Searched 2026-09-29: the tracker's full text for
  `shutdown_http_manager`, `RGWHTTPManager::stop`, `notify::shutdown`,
  `s_http_manager_mutex`, `rgw_http_req_data`, "Frontends paused" and
  realm-reload hang, deadlock and stuck; and ceph/ceph pull requests, by
  keyword and, for every open `rgw` pull request and every one closed since
  2025-10-01, by the files they change.
- **Found:** a period commit during phase 1 cluster work, 2026-09-29;
  analysed 2026-09-29; not reproduced.

## radosgw's realm reload waits out the notification manager's timers

- **Kind:** defect, fixed in v21.0.0, not on squid or tentacle.
  Unreproduced: derived from the source; the reload times measured fit it.
- **Evidence:**
  - At v19.2.6 `Manager::stop()` sets `shutdown`, releases the work guard and
    joins the worker (`rgw_notify.cc:755-759`), but cancels no timer.
    `process_queues` waits between queue-list reads on a timer of 30 s plus
    100-500 ms of jitter (`:645-661`, `Q_LIST_UPDATE_MSEC` at `:809`), and
    each owned queue's `cleanup_queue` on one of 30 s (`:296-299`, `:815`).
    The worker's `io_context.run()` (`:775`) returns only after they fire, so
    `rgw::notify::shutdown`, which a realm reload's teardown calls near its end
    (see the previous entry), waits up to about 30.5 s with the frontends
    paused.
  - The same at v20.2.4 (`rgw_notify.cc:767-771`, `:657-673`, `:821`,
    `:305-308`, `:827`, `:787`).
  - Ceph commit 433717a2480, "rgw/notifications: allow for graceful shutdown
    of notification manager", makes `stop()` wait 2 s for the worker and then
    stop its `io_context` (main `rgw_notify.cc:793-810`, 2026-09-29). It is
    in v21.0.0 and later.
  - Reloads measured on disposable clusters on 2026-09-29, each from the
    radosgw's "Frontends paused" log line to its "driver closed" line, took
    10.0, 12.9 and 28.1 s on v19.2.6 and 2.7, 3.7 and 26.5 s on v20.2.4, all
    within that bound.
- **Releases:** Squid and Tentacle, checked at v19.2.6 and v20.2.4 and at the
  squid and tentacle branch heads (a742f50616e, 2026-09-03; 9208ed9a291,
  2026-09-23). v21.0.0 and later are not affected.
- **rgw-go:** cannot shorten a coexisting radosgw's reload.
  `hack/rooket/populate.sh` allows 180 s for it. Phase 3's notification
  manager must cancel its timers when it stops.
- **Upstream:** [#71963](https://tracker.ceph.com/issues/71963), fixed on main
  by [ceph/ceph#63986](https://github.com/ceph/ceph/pull/63986); the issue's
  pull-request field names
  [ceph/ceph#63909](https://github.com/ceph/ceph/pull/63909), an unrelated
  bucket-logging change. The squid backport,
  [#72004](https://tracker.ceph.com/issues/72004), is New. The tentacle
  backport, [#72003](https://tracker.ceph.com/issues/72003), is marked
  Resolved, but the pull request it names,
  [ceph/ceph#66769](https://github.com/ceph/ceph/pull/66769), carries only
  bucket-logging changes, and the tentacle branch head still has the old
  `stop()`.
- **Found:** analysis of the previous entry's hang, 2026-09-29; derived from
  the source, not reproduced.

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
  watch2 segfault). The draft
  [ceph/ceph#72251](https://github.com/ceph/ceph/pull/72251) revives it with
  abhishek593's commit unchanged and fixes both #73564 and #80992: it resets
  `retries` on a successful re-registration, returns after a failed
  `unregister_watch()`, and schedules one `C_ReinitWatch` at a time through
  an atomic `reinit_pending` flag, with a move constructor added because
  `RGWSI_Notify` holds a `std::vector<RGWWatcher>`.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-018); verified 2026-09-27;
  derived from the source, not reproduced on a running cluster, which would
  take over 100 failures in one radosgw's lifetime.

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

## radosgw lets a matching referer grant replace the requester's own ACL grants

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `rgw_acl.cc`, `rgw_acl.h` and `rgw_acl_swift.cc` are the same
  blobs at v19.2.6 and v20.2.4, so each of their lines below holds at both.
  - `RGWAccessControlPolicy::verify_permission` asks `get_perm` for
    `perm | RGW_PERM_READ_OBJS | RGW_PERM_WRITE_OBJS` (`rgw_acl.cc:208`). An
    S3 grant holds at most FULL_CONTROL, so unless Swift's container flags
    are granted too, the flags `get_perm` gathers from the user map, the
    owner and the groups fall short of that mask, and it calls
    `get_referer_perm` whenever the request has a Referer (`:190-192`).
  - `get_referer_perm` starts from those flags and assigns each matching
    referer grant's flags over them (`:137-158`). A match therefore discards
    the requester's own grant, the owner's implicit READ_ACP and WRITE_ACP,
    and the group grants alike.
  - A Swift container ACL is built on `create_default` for the user who
    sets it (`rgw_rest_swift.cc:706-717`; v20.2.4 `:704-715`), normally the
    owner, who so keeps FULL_CONTROL, and each `.r:host` entry of its read
    list becomes a referer grant holding `RGW_PERM_READ_OBJS`
    (`rgw_acl_swift.cc:21`, `:57-96`, `:146-149`, `:180-191`).
  - The bucket ACL check passes the request's `HTTP_REFERER`, and `perm` as
    both the mask and the permission (`rgw_common.cc:1128-1130`,
    `:1420-1423`; v20.2.4 `:1141-1143`, `:1451-1454`). No check before it
    spares the owner (`:1342-1372`, `:1405-1409`; v20.2.4 `:1366-1393`,
    `:1435-1439`). An account user's requests to its own account's buckets
    never consult ACLs (`:1397-1403`; v20.2.4 `:1427-1433`), so an owner is
    affected only as a user outside any account.
  - So on a bucket whose Swift read ACL is `.r:example.com` and which has no
    bucket policy, the owner's S3 PutObject (`rgw_op.cc:3982-3985`; v20.2.4
    `:4191-4194`) with a `Referer: http://example.com/page` header is refused
    with 403: its WRITE, from FULL_CONTROL, is replaced by READ_OBJS, which
    counts only as READ and READ_ACP (`rgw_acl.cc:216-221`). Without the header
    the same request succeeds. Over Swift the owner's PUT still succeeds: the
    check falls back to the user ACL (`rgw_common.cc:1427-1430`; v20.2.4
    `:1461-1466`), which radosgw loads only for Swift, as the bucket owner's
    account ACL, by default FULL_CONTROL for the owner, and checks with no
    Referer (`rgw_op.cc:588-590`, `:453-482`; v20.2.4 `:618-620`, `:483-512`).
  - The replacement came with 11d4370eaf7, "rgw: partially respect Swift's
    negative, HTTP referer-based ACLs", so that a negative `.r:-host` grant,
    stored with no flags, could revoke what `.r:*` gives through the
    AllUsers group.
- **Releases:** every release since v12.1.0; checked at v19.2.6 and v20.2.4,
  at the squid and tentacle branch heads (a742f50616e, 2026-09-03;
  c9579b573bc, 2026-09-29), which carry the same `rgw_acl.cc` and
  `rgw_acl.h`, and at main (fb19b5bced2, 2026-09-30), whose two files differ
  only in their modelines, one include and `generate_test_instances`.
- **rgw-go:** mirrors it, so that either gateway answers a request on a
  shared zone alike: `acl.List.RefererPerm` replaces the flags as
  `get_referer_perm` does, and the spec "lets a matching referer grant
  replace even the owner's own grant, as radosgw does"
  (`internal/acl/eval_test.go`) pins it.
- **Upstream:** no issue or pull request reports it. Not filed: filing waits
  on a reproduction on a running system and a C++ reproducer.
  [#18841](https://tracker.ceph.com/issues/18841) (Resolved) is the
  negative-referer report the replacement answered, through
  [ceph/ceph#14344](https://github.com/ceph/ceph/pull/14344), after a first
  attempt, [ceph/ceph#13294](https://github.com/ceph/ceph/pull/13294), was
  closed unmerged. Searched 2026-09-29 and 2026-09-30, each method checked
  first on a known match:
  - the tracker's issue filter on any searchable field, every project and
    status, checked on `cls_otp_divzero_repro`, which only a comment on
    [#80948](https://tracker.ceph.com/issues/80948) holds: `get_referer_perm`
    and `referer_list` find nothing, `ACLReferer` only
    [#18685](https://tracker.ceph.com/issues/18685), its backport
    [#18895](https://tracker.ceph.com/issues/18895) and a cleanup
    ([#39619](https://tracker.ceph.com/issues/39619)), and `RGW_PERM_READ_OBJS`
    only [#18517](https://tracker.ceph.com/issues/18517), about Swift's default
    container ACLs. The tracker's full-text search for referer with acl, owner,
    denied, 403 and AccessDenied finds no report of it either; its closest hits
    are #18841 and #18685.
  - `gh search prs --repo ceph/ceph`, checked on a phrase that only
    #14344's body holds: `get_referer_perm` and `ACLReferer` find nothing,
    `referer_list` only #14344.
  - ceph/ceph pull requests by the files they change, for `src/rgw/rgw_acl.cc`
    and `src/rgw/rgw_acl.h`, checked on the merged #14344,
    [ceph/ceph#13005](https://github.com/ceph/ceph/pull/13005),
    [ceph/ceph#65374](https://github.com/ceph/ceph/pull/65374) and
    [ceph/ceph#65742](https://github.com/ceph/ceph/pull/65742): none of the 1561
    open ones touches either. Of the 3462 closed unmerged since 2024-01-01, only
    [ceph/ceph#43978](https://github.com/ceph/ceph/pull/43978) (the owner's
    `get_perm` flags widened to FULL_CONTROL, the referer step left as is) and
    [ceph/ceph#57539](https://github.com/ceph/ceph/pull/57539) (subuser
    permission masks) are ACL changes; the rest carry the two files among
    hundreds or thousands of others.
- **Found:** phase 1 unit Z, Task 6's transcription of `get_perm`,
  2026-09-29; derived from the source, not reproduced.

## radosgw ends a Referer's userinfo at an @ in its path

- **Kind:** defect, unfixed through main. Unreproduced: derived from the
  source.
- **Evidence:** `ACLReferer::get_http_host` (`rgw_acl.h:253-270`, the same
  blob at v19.2.6 and v20.2.4) takes what follows the first `://`, drops
  everything through the first `@` after it, wherever that `@` falls, and
  ends the host at the first `/` or `:`. An `@` in the path or query thus
  ends the userinfo: `http://evil.example/x@good.example/` has the host
  `good.example`. A query is not cut either: `http://example.com?x` has the
  host `example.com?x`. `is_match` compares that host with a referer
  grant's spec (`:212-233`), so a Swift referer ACL (`.r:host` or
  `.r:-host`) is decided by what follows the `@`, not by the page's host:
  with `.r:good.example` on a container, a request whose Referer is the URL
  above matches the grant. The parse came with 941dfad6717, "rgw: swift: The
  http referer should be parsed to compare in swift API".
- **Releases:** every release since v12.0.0; checked at v19.2.6, v20.2.4, the
  squid and tentacle branch heads and main, as for the previous entry.
- **rgw-go:** mirrors it: `acl.Referer.IsMatch` parses the host as
  `get_http_host` does, and the spec "the first @ after the scheme ends the
  userinfo, even in the path" (`internal/acl/eval_test.go`) pins it.
- **Upstream:** no issue or pull request reports it. Not filed, for the same
  reason as the previous entry. [#18685](https://tracker.ceph.com/issues/18685)
  (Resolved) asked for the Referer's host to be compared rather than the whole
  value; its fix, [ceph/ceph#13005](https://github.com/ceph/ceph/pull/13005),
  wrote this parse. Searched with the previous entry's methods: the tracker
  filter finds `get_http_host` only in
  [#39619](https://tracker.ceph.com/issues/39619), a string_view cleanup, and
  the full-text search for referer with userinfo, host parse and hostname finds
  only #18685, its backports and unrelated reports; `gh search prs` finds
  `get_http_host` only in #13005 and in
  [ceph/ceph#50330](https://github.com/ceph/ceph/pull/50330), an unrelated
  `RGWEnv::get` fix; the file sweep is the previous entry's.
- **Found:** phase 1 unit Z, Task 6's transcription of `is_match`,
  2026-09-29; derived from the source, not reproduced.

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
  zero. `trim_expired()` also reads `step_size`, for its replay window
  (`cls_otp.cc:100` at v19.2.6 and v20.2.4, main `:96`).
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
- **Upstream:** [#80948](https://tracker.ceph.com/issues/80948). Its fix,
  [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250), a draft, adds a
  file-local `effective_step_size()` that uses liboath's default time step
  (`OATH_TOTP_DEFAULT_TIME_STEP_SIZE`) for a stored 0, at the divide and in
  `trim_expired()`'s replay window, so it also guards entries already stored
  with 0.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-012); liboath premise and
  trigger verified 2026-09-27; confirmed in the source at v19.2.6, v20.2.4
  and main. A userspace reproducer that calls liboath alone, on
  [#80948's note-1](https://tracker.ceph.com/issues/80948#note-1), fails
  with SIGFPE (exit status 136) for a `step_size` of 0 and runs normally for
  30. No run on a live OSD was attempted: it would be a crash reproduction.

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
  after a later one within the window. No code outside the window is
  accepted, so it does not bypass MFA.
- **Releases:** every release checked, v19.2.2 through main.
- **rgw-go:** no cls_otp client yet (phase 2, MFA). If it reproduces the
  replay index it must take the direction from the position out-parameter,
  not from the return value.
- **Upstream:** [#80949](https://tracker.ceph.com/issues/80949), which we
  filed. Its fix, [ceph/ceph#72253](https://github.com/ceph/ceph/pull/72253),
  a draft, passes `&otp_pos` and sets the index to
  `(secs - otp.time_ofs) / otp.step_size + otp_pos`. It changes the same
  statement as [ceph/ceph#72250](https://github.com/ceph/ceph/pull/72250),
  #80948's fix; the two are independent, and whichever merges second needs a
  one-line rebase.
- **Found:** reported by rgw-rs (rados-rs CEPH-BUG-013); liboath premise
  verified 2026-09-27 and confirmed at oath-toolkit-2.6.11, whose
  `liboath/totp.c` returns the absolute position and carries its sign only in
  `otp_pos`. A userspace reproducer that calls liboath alone, run in a CentOS
  Stream 9 container and re-run independently, shows the defect and the fix
  side by side; it is attached to #80949. Not reproduced on a running
  cluster.

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

## The --name error lists the entity types as raw bytes

- **Kind:** defect, a regression new in v19.2.6 and v20.2.4; fixed on the
  squid and tentacle branches, in no release yet.
- **Evidence:**
  - `EntityName::get_valid_types_as_str` streams
    `STR_TO_ENTITY_TYPE[i].first`, an `entity_type_t`, which is a `uint8_t`
    (`entity_name.cc:145-155`, `msgr.h:89`, identical at v19.2.6 and
    v20.2.4), so it writes each type's byte instead of its name. Its one
    caller is the `-n`/`--name` error of `ceph_argparse_early_args`
    (`ceph_argparse.cc:528`; v20.2.4 `:532`), which `global_pre_init` runs
    for radosgw and every other program built on `global_init`.
  - Reproduced on the release images without a cluster, since early
    arguments are parsed before any configuration is read:
    `podman run --rm --network=none quay.io/ceph/ceph:v19.2.6 radosgw
    --name=bogus.x`, and the same on `v20.2.4`, both exit 1 with identical
    output, shown here through `cat -v`: `error parsing 'bogus.x': expected
    string of the form TYPE.ID, valid types are:  , ^A, ^D, ^B, ^P, ^H`. The
    list is the bytes 0x20, 0x01, 0x04, 0x02, 0x10 and 0x08, which are auth,
    mon, osd, mds, mgr and client. The v21.1.0 image prints `valid types are:
    auth, mon, osd, mds, mgr, client`.
  - The regression is main's ad8e5eb399a, "common/entity_name: cleanup
    entity_name::type", which made the table (type, name) pairs and changed
    the stream from `.str` to `.first`. It reached the floor releases as
    e03a5724798 on squid and f92ac6f3e6a on tentacle; v19.2.5, v20.2.3 and
    v21.1.0 print the names. Ceph commit 18e32faf20a, "common/entity_name:
    print type names in get_valid_types_as_str()", streams `.second`. Its
    `Fixes:` line names 0753b1d5326, which only adds a `type_str` field to
    `EntityName::dump`.
- **Releases:** v19.2.6 and v20.2.4, and main between ad8e5eb399a and
  18e32faf20a.
- **rgw-go:** unaffected. `cephconf.ParseEarly` rejects the same names and
  prints the type names: `valid types are: auth, mon, osd, mds, mgr,
  client`.
- **Upstream:** [#79678](https://tracker.ceph.com/issues/79678), resolved,
  lists "EntityName valid-types garbage" among the cephx merge's make-check
  fallout, fixed on main by
  [ceph/ceph#71165](https://github.com/ceph/ceph/pull/71165). Its squid
  backport is [#79640](https://tracker.ceph.com/issues/79640) with
  [ceph/ceph#71190](https://github.com/ceph/ceph/pull/71190), and its
  tentacle backport [#79638](https://tracker.ceph.com/issues/79638) with
  [ceph/ceph#71191](https://github.com/ceph/ceph/pull/71191); both backport
  trackers also carry the MonClient.h build break,
  [#79628](https://tracker.ceph.com/issues/79628), and bear its title. The
  three pull requests merged between 2026-08-19 and 2026-08-21. Neither
  backport tracker names a release: "Released In" is empty on both, and
  "Fixed In" is v19.2.6-238-g5e2e9bc161 and v20.2.4-36-g7364075cb8, commits
  past the floor tags, so no released Squid or Tentacle version carries the
  fix.
- **Found:** phase 1 unit G, early-argument parsing, 2026-09-29; reproduced
  2026-09-29 on the v19.2.6 and v20.2.4 images.

## radosgw ignores a bad port in an IPv6 endpoint

- **Kind:** defect, unfixed through main. Unreproduced on a running
  radosgw; a C++ reproducer of the function shows it.
- **Evidence:**
  - For an IPv6 `endpoint` or `ssl_endpoint`, `parse_endpoint` parses the
    port with `parse_port` and then the address with `make_address_v6`,
    passing both the same error code and checking it only after the
    second (`rgw_asio_frontend.cc:574` then `:580` at v19.2.6, `:526` then
    `:532` at v20.2.4, `:542` then `:548` on main at 7ed73efc1be). The IPv4
    branch returns on the port's error first (`:585-588` at v19.2.6).
  - boost asio's `socket_ops::inet_pton`, which `make_address_v6` calls,
    clears the last error on entry and after the system `inet_pton` sets
    the error code from errno, so a successful address parse resets the
    port's error (boost 1.82 `socket_ops.ipp:2537` and `:2765-2766`, 1.87
    `:2604` and `:2832-2833`; the tags build 1.82 and 1.87,
    `CMakeLists.txt:709` and `:723` at v19.2.6, `:754` and `:768` at
    v20.2.4).
  - `parse_port` returns what `strtoul` read, truncated to 16 bits, even
    when it sets the error (`:537-547` at v19.2.6). So `endpoint=[::1]:http`
    listens on port 0, an ephemeral port, and `endpoint=[::1]:70000` on port
    4464, where `endpoint=127.0.0.1:http` and `endpoint=127.0.0.1:70000`
    are refused.
  - A reproducer compiles `parse_port` and `parse_endpoint`, copied
    verbatim from v20.2.4 (`:489-548`, identical to v19.2.6 `:537-596`),
    against boost 1.83 and prints `[::1]:http -> ec=ok port=0`,
    `[::1]:70000 -> ec=ok port=4464`, `127.0.0.1:http -> ec=Invalid
    argument port=0` and `127.0.0.1:70000 -> ec=Numerical result out of
    range port=4464`.
- **Releases:** v13.2.3 and later on mimic, and v14.1.0 and later, through
  v19.2.6, v20.2.4 and main. The IPv6 branch came with a3b4124fd25, "rgw:
  beast frontend parses ipv6 addrs"
  ([ceph/ceph#24887](https://github.com/ceph/ceph/pull/24887), the fix for
  [#36662](https://tracker.ceph.com/issues/36662)), and its mimic
  backport, and has never checked the port's error.
- **rgw-go:** refuses an IPv6 endpoint whose port does not parse, as
  radosgw refuses an IPv4 one (`parseEndpoint`,
  `internal/frontend/spec.go`); `docs/exclusions.md` records the
  difference.
- **Upstream:** no tracker issue or pull request reports or fixes it.
  Searched 2026-09-29, each search first run on a known match:
  - the tracker's full text, all projects, for `parse_endpoint`,
    `parse_port` and `"endpoint=["` (known match: #36662's description);
  - ceph/ceph pull requests by keyword for `parse_endpoint` (known match:
    #24887), which finds only
    [ceph/ceph#66290](https://github.com/ceph/ceph/pull/66290) and
    [ceph/ceph#27008](https://github.com/ceph/ceph/pull/27008), neither of
    which changes the function;
  - every open ceph/ceph pull request, and every `rgw` pull request closed
    since 2025-10-01, by whether it changes `src/rgw/rgw_asio_frontend.cc`
    (known match: [ceph/ceph#68920](https://github.com/ceph/ceph/pull/68920)),
    with each such pull request's copy of `parse_endpoint` checked. None
    changes it; the ssl hot-reload pull requests only move its caller.
  On main, only 9e5d4bd1a5d (default ports) and ed70d843df4 (spelling)
  have changed the function since the IPv6 branch arrived. Not filed:
  filing waits on a reproduction on a running radosgw.
- **Found:** phase 1 frontend work (unit G), 2026-09-29; the reproducer
  was run in review; not reproduced on a running radosgw.

## Squid's radosgw fails to start when its realm search meets a realm whose period cannot be read

- **Kind:** defect, fixed on main and tentacle, unfixed on squid.
  Unreproduced: found by reading the code at v19.2.6.
- **Evidence:** at v19.2.6, when the zone is found but its realm's current
  period does not hold it, `RGWSI_Zone::do_start` searches every realm for
  the zone (`search_realm_with_zone`, `services/svc_zone.cc:178-192`). The
  search skips a realm it cannot open (`:105-109`) but returns the error of
  `RGWRealm::find_zone` (`:111-116`), which returns its period's init error
  although it logs "period init failed ... skipping"
  (`rgw_realm.cc:216-242`), and `do_start` then fails (`svc_zone.cc:189-191`).
  A realm with an empty current period is one such realm: `RGWPeriod::init`
  reads `periods..latest_epoch` for it and gets ENOENT
  (`rgw_period.cc:14-46`). The search runs for an empty current period too,
  so a realm without a readable period stops radosgw from starting,
  although SiteConfig::load, which runs first, falls back to the local
  zonegroup for it (`driver/rados/rgw_zone.cc:1239-1250`). radosgw-admin
  writes an initial period with every realm it creates
  (`driver/rados/rgw_zone.cc:513-522`), so the shape needs a period removed
  or a realm written outside it.
- **Releases:** squid. v20.2.4 has no realm search: `RGWSI_Zone::do_start`
  takes the zonegroup and zone from SiteConfig
  (`services/svc_zone.cc:98-103` at v20.2.4).
- **rgw-go:** searches no realm. Without a readable period it takes the
  local zonegroup and starts, as Tentacle does (`docs/exclusions.md`,
  "Zone and placement differences").
- **Upstream:** fixed on main by
  [ceph/ceph#63266](https://github.com/ceph/ceph/pull/63266) for
  [#71291](https://tracker.ceph.com/issues/71291), which reports another
  failure of the same search, "incorrect zonegroup" with two realms: it
  removes `search_realm_with_zone` with the rest of `do_start`'s duplicated
  startup logic. Backported to tentacle by
  [ceph/ceph#66300](https://github.com/ceph/ceph/pull/66300)
  ([#73890](https://tracker.ceph.com/issues/73890)) before v20.2.4. No
  squid backport: #71291's only copy is tentacle's, and origin/squid
  (a742f50616e, 2026-09-03) still calls the search. No issue reports this
  failure (searched 2026-09-30); each search was first run on a known
  match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#80948](https://tracker.ceph.com/issues/80948) by
    `oath_totp_validate4_callback`, a word only in its description. Terms:
    `search_realm_with_zone` (only
    [#56313](https://tracker.ceph.com/issues/56313), a crash while listing
    realms), `search_realm_conf`, `RGWRealm::find_zone`, `realm.find_zone`
    and `can't open realm skipping` (none), `period init failed skipping`
    (four unrelated issues) and `searching for the correct realm` (three
    test logs).
  - ceph/ceph pull requests through `gh search prs`, which finds #63266
    and #66300 by "remove duplicated startup logic". Terms:
    `search_realm_with_zone` (#63266 and the configstore refactor #62398),
    `period init failed` (#62398), `find_zone` (closed zipper work in
    progress only) and `RGWRealm find_zone` (none).
- **Found:** phase 1 metadata work (unit M), 2026-09-29, reading
  `RGWSI_Zone::do_start` for rgw-go's zone resolution.

## radosgw-admin bucket rm exits 0 when it removes nothing

- **Kind:** defect.
- **Evidence:**
  - `radosgw-admin bucket rm` calls `RGWBucketAdminOp::remove_bucket` and
    discards what it returns (`rgw_admin.cc:8768-8778` at v19.2.6,
    `radosgw-admin/radosgw-admin.cc:9226-9237` at v20.2.4).
    `remove_bucket` returns the error of finding the bucket and of removing
    it (`driver/rados/rgw_bucket.cc:1289-1308` at v19.2.6; the lookup is at
    `:1451-1455` at v20.2.4), so the command exits 0 whether or not it
    removed the bucket.
  - Reproduced on the rooket clusters on 2026-09-29, with the site options.
    On rgw-go-squid (19.2.6), `bucket rm --bucket s3t-leftover-tenant
    --purge-objects`, for a bucket that exists only as
    `s3tt/s3t-leftover-tenant`, printed nothing and exited 0, and the
    bucket stayed listed. On rgw-go-tentacle (20.2.4), `bucket rm --bucket
    no-such-bucket-rgw-go --purge-objects` exited 0, while `bucket stats`
    on the same name failed with `(2002)` and exit status 2.
- **Releases:** v19.2.6, v20.2.4 and main (7ed73efc1be, 2026-09-25,
  `radosgw-admin/radosgw-admin.cc:9853-9864`).
- **rgw-go:** unaffected as a gateway: the tool is Ceph's. The s3-tests
  harness removes buckets with it, so `hack/s3tests/run.sh` lists the
  user's buckets again afterwards and stops when one is left, and it names
  a tenant's bucket `tenant/name`, the form `bucket rm` finds.
- **Upstream:** no issue reports it (searched 2026-09-30); each search
  method was first run on a known match. Upstream main's own exit-code
  tests record the behaviour without calling it a bug:
  `src/test/rgw/radosgw-admin/test-bucket-exit-codes.sh`, added in
  ad8b642adad (2026-09-15, in no release), says "rm ignores the op's return
  value, so missing --bucket silently exits 0" (`:835`) and expects exit 0
  from `bucket rm --bucket=no-such-bucket --purge-objects` (`:222`), while
  it marks 42 known bugs of other commands `XFAIL`. The nearest report is
  [#80577](https://tracker.ceph.com/issues/80577), Fix Under Review, on the
  exit codes of `bucket stats`, `bucket list`, `bucket reshard` and
  `bucket set-min-shards`; its fix,
  [ceph/ceph#71858](https://github.com/ceph/ceph/pull/71858) (open), leaves
  `bucket rm` as it is.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#79678](https://tracker.ceph.com/issues/79678) by `valid-types
    garbage`. Terms: `bucket rm exit code`, `bucket rm return code`,
    `bucket rm exit status`, `bucket rm nonexistent`, `bucket rm
    non-existent`, `bucket rm does not exist`, `bucket rm silently`,
    `remove_bucket return value`, `radosgw-admin exit code 0` and
    `radosgw-admin returns 0 error`: #80577 and unrelated issues only.
  - ceph/ceph pull requests through `gh search prs`, which finds #71165 by
    `get_valid_types_as_str`. Terms: `BUCKET_RM` (none) and `80577` (#71858).
    The multi-word terms were rerun on 2026-09-30 with each word its own
    argument and `bucket rm` a phrase, a method that finds
    [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609) by `guard against
    dir suggest during reshard`: `"bucket rm" radosgw-admin`, `remove_bucket
    radosgw-admin return`, `radosgw-admin "bucket rm" exit` and `bucket rm
    purge-objects` find bypass-gc and multisite changes to the removal and the
    2019 fixes for a `--purge-objects` hang; `radosgw-admin bucket commands
    return codes`, `radosgw-admin return code` and `radosgw-admin exit code`
    find #71858, [ceph/ceph#71155](https://github.com/ceph/ceph/pull/71155)
    (exit-code tests for the command-line handling) and the open radosgw-admin
    refactors [ceph/ceph#69145](https://github.com/ceph/ceph/pull/69145) and
    [ceph/ceph#71462](https://github.com/ceph/ceph/pull/71462), whose `bucket
    rm` still discards what `remove_bucket` returns. None changes `bucket rm`'s
    exit status.
- **Found:** phase 1 unit T, the s3-tests harness's bucket purge,
  2026-09-29.

## radosgw's S3 ListBuckets reads neither max-buckets nor continuation-token

- **Kind:** defect, fixed on main after v20.2.4. Unreproduced on a running
  radosgw; found by reading the source.
- **Evidence:**
  - `RGWListBuckets_ObjStore_S3::get_params` sets `limit = -1` and reads
    no query parameter (`rgw_rest_s3.h:133-136` at v19.2.6, `:134-137` at
    v20.2.4), so `max-buckets`, `continuation-token` and `prefix` are
    ignored and every ListBuckets returns all of the owner's buckets.
  - `send_response_begin`, `send_response_data` and `send_response_end`
    write the owner and the buckets and no `ContinuationToken` or `Prefix`
    element (`rgw_rest_s3.cc:1511-1547` at v19.2.6, `:1622-1658` at
    v20.2.4).
  - A client that pages with `max-buckets` gets every bucket in its first
    response and no token to continue from.
- **Releases:** v19.2.6 and v20.2.4. On main, e0d797da790, merged as
  c531eec199d, reads `max-buckets` and `continuation-token` and writes
  `ContinuationToken`; it leaves `prefix` unread.
- **rgw-go:** reproduces it on both releases: its ListBuckets reads none of
  the three parameters and writes neither element (`listBuckets`,
  `internal/s3/listbuckets.go`). Revisit when a floor release carries the
  fix.
- **Upstream:** [#72315](https://tracker.ceph.com/issues/72315), "support
  paginated s3 ListBuckets", resolved on main by
  [ceph/ceph#64742](https://github.com/ceph/ceph/pull/64742); no squid or
  tentacle backport issue or pull request exists. `prefix` is
  [#75463](https://tracker.ceph.com/issues/75463), open, with
  [ceph/ceph#67920](https://github.com/ceph/ceph/pull/67920). Searched
  2026-09-30; each search was first run on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #72315 by
    `continuation-token`, a word only in its description. Terms:
    `max-buckets`, `continuation-token` and `ListBuckets` (#72315, #75463,
    and the resolved #57901 and its backports for the 1000-bucket
    truncation; nothing else about pagination).
  - ceph/ceph pull requests through `gh search prs`, which finds #64742 by
    "paginated ListBuckets". Terms: `paginated ListBuckets` (#64742 only)
    and `ListBuckets` in titles (#64742 and the truncation fix
    [ceph/ceph#48559](https://github.com/ceph/ceph/pull/48559) with its
    backports).
- **Found:** phase 1 gateway work (unit G), 2026-09-30, reading
  `RGWListBuckets_ObjStore_S3` for rgw-go's ListBuckets.

## url_decode reads outside its hex table for a byte above 0x7f after "%"

- **Kind:** defect, unfixed through main, with a fix in review. Reproduced
  by calling the `url_decode` that the librgw2 19.2.6 and 20.2.4 packages
  ship, and under AddressSanitizer; not reproduced on a running radosgw.
- **Evidence:**
  - `hex_to_num` looks an escape's digits up in `HexTable`'s 256-byte
    table with the `char` cast to `int` (`rgw_common.cc:1690-1692` at
    v19.2.6, `:1753-1755` at v20.2.4, `:1779-1781` on main at
    a956c21a8c9). ceph compiles every build with `-fsigned-char`
    (`src/CMakeLists.txt:95` at v19.2.6, `:102` at v20.2.4), so on every
    architecture a byte from 0x80 to 0xff indexes from -128 to -1 and
    reads the 128 bytes before the table.
    `url_decode` takes a negative value as a bad digit and empties its
    result, and uses any other as the digit (`:1701-1734` at v19.2.6,
    `:1764-1797` at v20.2.4).
  - A program `dlopen`s `librgw.so.2` from librgw2-19.2.6-0.el9 and
    librgw2-20.2.4-0.el9 and calls the exported `url_decode` on `%`, each
    byte from 0x80 to 0xff, and `0`. In 19.2.6, 104 of the 128 bytes read
    as the digit 0, 21 as other digits and 3 (0x90, 0x98, 0xe0) as none;
    in 20.2.4, 102 as 0, 17 as others and 9 as none. `%\xc3\xbc` decodes
    to a NUL byte in both. What the read finds depends on the build.
  - A reproducer that copies `HexTable`, its lookup and `url_decode`
    verbatim, built with `-fsigned-char -fsanitize=address`, reports a
    global-buffer-overflow read in the lookup for `%` followed by any byte
    above 0x7f (index -128 for 0x80), and runs clean with the fix below.
- **Releases:** v19.2.6, v20.2.4 and main (a956c21a8c9, 2026-09-30).
- **rgw-go:** takes such a byte as no hex digit, so the result is empty,
  as for any other bad digit; `docs/exclusions.md` records the
  difference.
- **Upstream:** we filed [#81267](https://tracker.ceph.com/issues/81267),
  which cites #4755, rather than comment on a resolved issue from 2013. The
  fix in review is [ceph/ceph#72272](https://github.com/ceph/ceph/pull/72272)
  (draft), a one-line change that indexes the table with the byte as
  `unsigned char`, so a byte above 0x7f finds the table's in-bounds -1 and
  is refused as a bad digit. Not found by us: #4755 reported it first.
  [#4755](https://tracker.ceph.com/issues/4755) (2013, Resolved), on
  `url_decode` assuming a signed `char` on armhf, remarked in passing that
  `hex_to_num` "has an obvious related bug in places where char is _not_
  unsigned"; the lookup it names still reads outside the table. Searched
  2026-09-30, each search first run on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds
    [#81000](https://tracker.ceph.com/issues/81000) by `dir suggest during
    reshard`. Terms: `url_decode` (#4755 and reports on `+` and on policy
    decoding), `hex_to_num` (#4755,
    [#6672](https://tracker.ceph.com/issues/6672) on the table's
    initialization race, and #8702 on `+`), `HexTable` (none),
    `percent-encoded`, `bad escape` and `escape sequence` (unrelated
    issues only).
  - ceph/ceph pull requests through `gh search prs`, which finds
    [ceph/ceph#59609](https://github.com/ceph/ceph/pull/59609) by `guard
    against dir suggest during reshard`. Terms: `hex_to_num`
    ([ceph/ceph#783](https://github.com/ceph/ceph/pull/783), the
    initialization race), `HexTable`, `invalid url-encoding` and `rgw
    signed char` (none), and `url_decode` (23 pull requests; none changes
    `hex_to_num`).
- **Found:** phase 1 auth work (unit A), transcribing `url_decode`,
  2026-09-29; the shipped libraries were called the same day. The
  rgw-bug-reproduction session reproduced it under AddressSanitizer on
  2026-09-30, and attached the reproducer to the tracker issue.

## A malformed percent-escape in the path makes radosgw serve another path

- **Kind:** defect, unfixed through main, with a fix in review. Reproduced
  on a running radosgw at v19.2.6 and v20.2.4.
- **Evidence:**
  - `RGWREST::preprocess` decodes the path with `url_decode` and checks
    the result only for a NUL (`rgw_rest.cc:2182-2186` at v19.2.6,
    `:2204-2208` at v20.2.4). `url_decode` returns an empty string for an
    escape with a character that is no hex digit, such as `%zz`, and stops
    at a `%` with fewer than two characters after it
    (`rgw_common.cc:1725-1726` and `:1718-1719` at v19.2.6, `:1788-1789`
    and `:1781-1782` at v20.2.4), so `/b/k%` and `/b/k%4` decode as
    `/b/k`. A virtual-hosted request's bucket is put in front of the path
    before the decode (`rgw_rest.cc:2154-2161` at v19.2.6), so it is
    emptied with the rest.
  - The S3 handler reads an empty path as naming neither bucket nor
    object (`init_from_header`, `rgw_rest_s3.cc:4908-4914` at v19.2.6,
    `:5468-5474` at v20.2.4). So `GET /b/%zz` is served as `GET /`, a
    ListBuckets, and a request for the key `k%` acts on the key `k`.
  - SigV4 signs the same decode: the canonical URI is `aws4_uri_recode`
    of the path, and `/` when that is empty (`get_v4_canonical_uri`,
    `rgw_auth_s3.h:598-612` at v19.2.6, `:601-615` at v20.2.4), so the
    signature such a request needs is one over the path radosgw serves.
- **Releases:** v19.2.6, v20.2.4 and main (`url_decode` still returns an
  empty string, `rgw_common.cc:1815` at a956c21a8c9).
- **rgw-go:** net/http answers such a request `400 Bad Request` before any
  handler runs; `docs/exclusions.md` records the difference under
  "Frontend differences".
- **Upstream:** we filed [#81301](https://tracker.ceph.com/issues/81301),
  which cites #71458 and ceph/ceph#63521, rather than comment on the
  resolved CVE. The fix in review is
  [ceph/ceph#72301](https://github.com/ceph/ceph/pull/72301) (draft): a
  strict `url_decode` that reports a malformed escape, which the path's
  routing callers refuse with 400, plus unit tests. The lenient `url_decode`
  stays as it is, so #71458's empty copy-source check still holds. Not found
  by us: #71458 reported the same `url_decode` result first, through
  another caller.
  [#71458](https://tracker.ceph.com/issues/71458) (CVE-2025-48052,
  Resolved) was the same empty result crashing UploadPartCopy through
  `x-amz-copy-source`; its fix,
  [ceph/ceph#63521](https://github.com/ceph/ceph/pull/63521), guards that
  caller in `rgw_op.cc` and changes `url_decode` only by a blank line.
  Searched 2026-09-30 with the methods and known matches of the entry
  above: tracker terms `url encoding`, `invalid url`, `malformed url`,
  `percent sign` and `bad escape` find #71458, its backports and
  unrelated issues; pull request terms `url_decode` and `invalid
  url-encoding` find no fix for the path.
- **Found:** phase 1 auth work (unit A), checking the frontend's
  percent-escape entry against `url_decode`, 2026-09-30. The
  rgw-bug-reproduction session reproduced it on running v19.2.6 and v20.2.4
  clusters the same day. `GET /repro/k%` returns the body of key `k`, while
  `GET /repro/k%25` answers 404. `GET /repro/%zz` returns a ListAllMyBuckets
  result for a URL that names an object in bucket `repro`. The radosgw log
  shows the routing on the decoded path. The reproducer, written in Go, its
  output and the log are attached to the tracker issue.

## radosgw skips or never finishes every bucket-index batch on a zero rgw_bucket_index_max_aio

- **Kind:** defect, unfixed through main.
- **Evidence:** `rgw_bucket_index_max_aio`, the number of bucket-index
  operations radosgw keeps in flight across a bucket's shards, is a `uint`
  with no `min:` (`common/options/rgw.yaml.in:195-202` at v19.2.6,
  `:201-208` at v20.2.4), so a configured 0 is accepted.
  - v19.2.6: `CLSRGWConcurrentIO::operator()` issues shard operations while
    `max_aio-- > 0` (`cls/rgw/cls_rgw_client.cc:28`), so a zero issues none.
    `BucketIndexAioManager::wait_for_completions` then returns false because
    nothing is pending (`:139-141`), and the batch returns 0. Bucket-index
    initialization on bucket creation (`services/svc_bi_rados.cc:369-371`)
    and listing (`driver/rados/rgw_rados.cc:9710-9713`) are among the batches
    that report success without touching a shard.
  - v20.2.4: each batch reads the option into a `size_t` and hands it to
    `rgwrados::shard_io` as its window, as the index-header read does
    (`services/svc_bi_rados.cc:374-391`). Before sending a shard operation,
    the reader and both writers wait for a completion while
    `outstanding.size() >= max_concurrent` (`driver/rados/shard_io.h:353`,
    `:480` and `:603`), which a zero window satisfies with nothing
    outstanding. `async_wait` only parks the caller (`:213-226`), and only a
    shard operation's completion wakes it (`maybe_complete`, `:228-238`), so
    the batch never finishes: a request's coroutine stays suspended, and a
    blocking caller waits forever (`svc_bi_rados.cc:389-390`).
- **Releases:** v19.2.6 skips the batch; v20.2.4 and main (a956c21a8c9,
  2026-09-30, `rgw.yaml.in:232-239`, `shard_io.h:359`, `:486` and `:610`)
  never finish it.
- **rgw-go:** reads the option once at startup and uses a zero as 1, with an
  error-level log line naming the option, so the driver's bucket-index
  fan-out always has a window. `docs/exclusions.md` records the difference.
- **Upstream:** no issue or pull request reports it (searched 2026-09-30);
  each search method was first run on a known match. Not filed: filing waits
  on a reproduction on a running system.
  [ceph/ceph#72160](https://github.com/ceph/ceph/pull/72160), the fix for
  [#80991](https://tracker.ceph.com/issues/80991), adds `min: 1` to
  `rgw_lc_max_objs`, `rgw_usage_max_shards` and `rgw_gc_max_objs` only.
  - tracker.ceph.com: the full-text search of every project's issues, open
    and closed, which finds #80991 by `rgw_usage_max_shards`. Terms:
    `rgw_bucket_index_max_aio`, `bucket_index_max_aio`,
    `CLSRGWConcurrentIO` and `max_concurrent shard_io`: no results.
  - ceph/ceph pull requests through `gh search prs`, which finds #72160 by
    `rgw_usage_max_shards`. Term `rgw_bucket_index_max_aio`: six pull
    requests (#28558, #49795, #59199, #59222, #60628 and #61760), none
    about a zero window.
- **Found:** phase 1 unit M, validating the driver's options, 2026-09-30;
  not reproduced on a running cluster.

## radosgw clamps a copy of rgw_override_bucket_index_max_shards that bucket creation never reads

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - `rgw_override_bucket_index_max_shards` is a `uint` with no `max:`
    (`common/options/rgw.yaml.in:177-193` at v19.2.6, `:183-199` at
    v20.2.4).
  - `RGWRados::init_complete` copies it, or the zone's
    `bucket_index_max_shards` when it is 0, into the member
    `bucket_index_max_shards`, lowers the copy to `get_max_bucket_shards()`,
    65521, and logs "bucket index max shards is too large, reset to value:
    65521" (`driver/rados/rgw_rados.cc:1334-1341` at v19.2.6, `:1323-1330`
    at v20.2.4; `services/svc_bi_rados.h:33` and `:94-96` at v19.2.6, `:34`
    and `:95-97` at v20.2.4). Nothing else in `src/rgw` uses that member,
    declared at `rgw_rados.h:401` (v19.2.6) and `:413` (v20.2.4).
  - Bucket creation reads the option itself: `init_default_bucket_layout`
    gives the new index that many shards when it is above 0, and the zone's
    `bucket_index_max_shards` otherwise, neither clamped
    (`driver/rados/rgw_bucket.cc:2790-2796` at v19.2.6; `:2905-2914` at
    v20.2.4, where a shard count the caller passes comes first, also
    unclamped).
  - Above 7877 shards, `rgw_shards_mod` reduces the key's hash modulo 65521
    before the shard count (`driver/rados/rgw_tools.h:46-55` at v19.2.6,
    `:63-72` at v20.2.4), so a bucket created with more than 65521 shards
    never places an entry on a shard above 65520: 65522 shards leave 1
    empty, 70000 leave 4479. `radosgw-admin bucket reshard` refuses more
    than 65521 shards (`rgw_admin.cc:3027-3030` at v19.2.6,
    `radosgw-admin/radosgw-admin.cc:3224-3227` at v20.2.4), and dynamic
    resharding caps its target there (`rgw_rados.cc:10591` at v19.2.6,
    `:11521` at v20.2.4); bucket creation does not.
- **Impact:** the log line reports a limit that is not applied, and a bucket
  created while the option is above 65521 has index shards that never hold
  an entry.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_rados.cc:1408-1412` and `rgw_bucket.cc:3088-3090`); older releases
  not checked.
- **rgw-go:** reads the option once at startup, without radosgw's clamp
  (`internal/driver/options.go`); nothing in rgw-go reads it yet.
- **Upstream:** [#70980](https://tracker.ceph.com/issues/70980), "a bucket
  can only effectively use up to 65,521 bucket index shards", reports it. It
  was rejected on 2025-04-18 once `radosgw-admin bucket reshard` was shown to
  refuse 65522 shards; bucket creation through this option was not checked
  there, and resharding looked protected because it clamps its own count
  while the per-bucket layout never reads the clamped `RGWRados` copy. We
  commented on #70980 with a reproducer of the creation path rather than
  file a new issue. The fix,
  [ceph/ceph#72256](https://github.com/ceph/ceph/pull/72256), a draft, clamps
  both creation branches, the option and a count the caller passes, to
  `rgw_shards_max()`, as resharding and `RGWRados` already do; an option
  `max:` would instead reject an existing configuration on upgrade. No other
  issue or pull request reports it (searched 2026-09-30); each search method
  was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `rgw_override_bucket_index_max_shards`
    (fifteen issues, none about its bound), `get_max_bucket_shards` and the
    log line as a quoted phrase (none), and `65521` (#70980; #20934 with its
    backports, a fixed bug that resharded buckets to 65521 shards; and
    unrelated QA reports).
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms:
    `rgw_override_bucket_index_max_shards` (fourteen pull requests),
    `init_default_bucket_layout` (six) and the words of the log line
    (21), none bounding the option at bucket creation, and
    `get_max_bucket_shards` (none).
- **Found:** phase 1 unit M, reading the driver's options, 2026-09-30. A
  userspace reproducer that copies `rgw_shards_mod` verbatim, re-run
  independently, shows the unreachable shards; it is attached to #70980. A
  run on a live cluster was deferred, the source and the arithmetic being
  conclusive.

## radosgw wraps an rgw_cache_expiry_interval above 18446744073 seconds

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - `rgw_cache_expiry_interval` is a `uint` count of seconds with no `max:`,
    default 900, where 0 turns expiry off
    (`common/options/rgw.yaml.in:3324-3336` at v19.2.6, `:3509-3521` at
    v20.2.4).
  - The object cache (`ObjectCache::set_ctx`) and each chained cache
    (`RGWChainedCacheImpl::init`) assign
    `std::chrono::seconds(get_val<uint64_t>("rgw_cache_expiry_interval"))`
    to an expiry of type `ceph::timespan` (`rgw_cache.h:173` and `:210-211`
    at v19.2.6, `:174` and `:211-212` at v20.2.4;
    `services/svc_sys_obj_cache.h:150` and `:174-175` at v19.2.6, `:152`
    and `:176-177` at v20.2.4). `ceph::timespan` counts nanoseconds in a
    `uint64_t` (`common/ceph_time.h:66` and `:74` at v19.2.6, `:63` and
    `:71` at v20.2.4), so the conversion multiplies by 10^9 modulo 2^64: any
    value above 18446744073 s, about 584 years, wraps.
  - A lookup misses once its entry is older than the result
    (`rgw_cache.cc:30-31` at both tags; `svc_sys_obj_cache.h:184-187` at
    v19.2.6, `:186-189` at v20.2.4). Compiled with the same types (g++
    15.3.1, x86-64): 18446744074 s becomes 0.29 s, so an entry expires 0.29 s
    after it is cached; any multiple of 2^55 s, 2^63 s among them, becomes
    0, which the `expiry.count()` guard reads as expiry off, so entries
    never expire;
    2^64-1 s, whose `std::chrono::seconds` count is -1, becomes 2^64 - 10^9
    ns, about 584 years, so it expires nothing.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_cache.h:174` and `:211`, `svc_sys_obj_cache.h:152` and `:176`); older
  releases not checked.
- **rgw-go:** reads the option once at startup and holds any value above
  about 9.2e9 s as the longest Duration, about 292 years
  (`internal/driver/options.go`), so it never wraps. No rgw-go cache reads
  it yet; the one that does records the difference in `docs/exclusions.md`.
- **Upstream:** [#81218](https://tracker.ceph.com/issues/81218), which we
  filed. Its fix, [ceph/ceph#72255](https://github.com/ceph/ceph/pull/72255),
  a draft, gives the option `min: 0` and `max: 17_G` in `rgw.yaml.in`, so
  `Option::validate()` rejects an overflowing value when it is set, for both
  sites. No earlier issue or pull request reports it (searched 2026-09-30);
  each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `rgw_cache_expiry_interval` and
    `cache_expiry_interval` (twelve issues: multisite cache-coherence reports
    and backports, #77531, #77554, #77731, #80389 and #80864 through #80866;
    #24346, entries never refreshed after the interval, fixed by
    [ceph/ceph#22324](https://github.com/ceph/ceph/pull/22324), with its
    backports; and a luminous performance regression), and `expiry miss`
    (thirteen, none about the option). None reports the conversion.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms:
    `rgw_cache_expiry_interval` (nine pull requests: #22324 with its
    backports, the cache-version changes #69748 and #71143, #46304 and
    release notes) and `cache_expiry_interval` (none). None changes the
    conversion.
- **Found:** phase 1 unit M, reviewing the driver's options, 2026-09-30; the
  conversion was checked by compiling it with radosgw's types. A
  self-contained C++17 reproducer that models `ceph::timespan`, run in a
  container against a control and re-run independently, shows the wrap; it
  is attached to #81218. Not reproduced on a running cluster.

## radosgw spins or stops caching on a negative usage-log or quota interval

- **Kind:** defect, unfixed through main.
- **Evidence:** `rgw_usage_log_tick_interval`, `rgw_bucket_quota_ttl`,
  `rgw_user_quota_bucket_sync_interval`, `rgw_user_quota_sync_interval` and
  `rgw_user_quota_sync_wait_time` are `int` counts of seconds with no `min:`
  (`common/options/rgw.yaml.in:1651`, `:2168`, `:2262`, `:2275` and `:2298`
  at v19.2.6; `:1734`, `:2260`, `:2362`, `:2375` and `:2398` at v20.2.4),
  so a negative value is accepted. What radosgw does with one, the same at
  both tags unless noted:
  - Usage-log tick: each firing of the usage logger's timer flushes and
    re-arms it `rgw_usage_log_tick_interval` seconds later
    (`rgw_log.cc:105-117`). `add_event_after` converts the seconds to a
    signed `signedspan` and adds it to the monotonic clock as an unsigned
    `timespan` (`common/Timer.cc:137-150`; `common/ceph_time.h:467-470` at
    v19.2.6, `:464-467` at v20.2.4), so a negative interval schedules the
    event that far in the past, and the timer thread flushes without pause.
  - Stats-cache TTL: `RGWQuotaCache::set_stats`, which the bucket and owner
    stats caches share, adds the TTL, and half of it, to the current time
    (`rgw_quota.cc:136-139`) through `utime_t`'s `operator+=(double)`, which
    casts the seconds to `__u64` (`include/utime.h:528-535`). C++ leaves
    that cast undefined for a negative value; with GCC on x86-64 it wraps,
    so `expiration` lands that far before now and `get_stats` never returns
    a cached stat, reading it from RADOS on every quota check
    (`rgw_quota.cc:158-164`). Below about -1.79e9 s, the current Unix time,
    the sum wraps below zero instead, and `cap_to_u32_max` pins
    `expiration` at 2^32-1, in 2106, so the cached stat never expires.
  - Bucket-sync interval: at v19.2.6 `BucketsSyncThread` waits
    `std::chrono::seconds(rgw_user_quota_bucket_sync_interval)`
    (`rgw_quota.cc:378-381`), which a negative makes return at once, so the
    thread loops without pause. v20.2.4 converts the value to `uint64_t`,
    multiplies it by a `double` factor and waits at least 1 s
    (`rgw_quota.cc:394-401`); with the default factor of 1 and g++ on
    x86-64, -1 through -1024 wait 1 s, and anything lower converts back to a
    negative wait and spins.
  - Owner-sync interval: `OwnerSyncThread` waits
    `std::chrono::seconds(rgw_user_quota_sync_interval)`
    (`rgw_quota.cc:427-428` at v19.2.6, `:448-449` at v20.2.4), so a
    negative makes it run `sync_all_owners` without pause.
  - Sync wait: `RGWOwnerStatsCache::sync_owner` adds the wait to the owner's
    last full sync (`rgw_quota.cc:642-648` at v19.2.6, `:663-669` at
    v20.2.4), which a negative puts before now, so every owner the pass does
    not skip as idle gets a full sync on every pass.
  A zero spins the tick, the owner-sync thread and v19.2.6's bucket-sync
  thread too; at v20.2.4 and main a zero bucket-sync interval waits 1 s, as
  -1 through -1024 do, up to 180 times as often as the default. A zero TTL
  or sync wait acts as a small negative one does. The conversions were
  checked by compiling them with radosgw's types (g++ 15.3.1, x86-64, at -O2
  and -O0).
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_log.cc:116`, `rgw_quota.cc:137-138`, `:392-399`, `:447` and `:663`,
  and still no `min:`); older releases not checked.
- **rgw-go:** reads each option once at startup, keeping a negative value
  (`internal/driver/options.go`); nothing in rgw-go consumes them yet.
- **Upstream:** [#81226](https://tracker.ceph.com/issues/81226), which we
  filed. Its fix, [ceph/ceph#72261](https://github.com/ceph/ceph/pull/72261),
  a draft, adds `min: 1` to all five options.
  [#80991](https://tracker.ceph.com/issues/80991) and
  [#48678](https://tracker.ceph.com/issues/48678) are related but not
  duplicates. No earlier issue or pull request reports it (searched
  2026-09-30); each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: the five option names, two to five issues
    each (unrelated reports that mention the options, and older quota and
    usage-log bugs), none about a negative value.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms: the five option names,
    none to six pull requests each (option descriptions in #71303 and
    #13395, and older quota changes), none bounding them.
- **Found:** phase 1 unit M, reviewing the driver's options, 2026-09-30. A
  self-contained C++17 reproducer that compiles the real expressions with
  GCC 11.5 at v19.2.6, v20.2.4 and main confirms each effect; it is attached
  to #81226. Not reproduced on a running cluster.

## radosgw's ARN conditions compare each ARN component with the text after it

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - `match_policy` walks a pattern and an input colon by colon, but takes
    each piece as `input.substr(last_pos_input, cur_pos_input)` and
    `pattern.substr(last_pos_pattern, cur_pos_pattern)`
    (`rgw_common.cc:2178-2179` at v19.2.6, `:2241-2242` at v20.2.4).
    `substr`'s second argument is a length, and it is given the next
    colon's position, so every piece after the first runs past its colon
    into the components after it, cut at a different length in the
    pattern and in the input, and `match_wildcards` compares those.
  - `arn_like` calls it for `ArnEquals` and `ArnLike`, and negated for
    `ArnNotEquals` and `ArnNotLike` (`rgw_iam_policy.cc:845-852` and
    `:996-1001` at v19.2.6, `:864-871` and `:1000-1005` at v20.2.4),
    although its comment says each of the six components is checked
    separately. So neither `arn:aws:sns:*:123456789012:topic` nor
    `arn:aws:sns:us-east-1:*:topic` matches
    `arn:aws:sns:us-east-1:123456789012:topic`, `arn:aws:s3:*:*:bucket`
    does not match `arn:aws:s3:::bucket`, `arn:aws:s3:::bucket/*/x` does
    not match `arn:aws:s3:::bucket/abc/x`, and `x:a\:y` matches `x:a:y`.
    A pattern whose only wildcard ends it, such as
    `arn:aws:s3:::bucket/*`, still compares correctly. Action matching is
    unaffected: every action name has one colon.
  - Verified by compiling radosgw's `match_policy` unchanged in the
    v19.2.6 and v20.2.4 images and running generated cases; not
    reproduced on a running radosgw. Of 116,003 generated cases whose
    pattern and input hold the same number of colons, two or more, 38,485
    answer differently from matching each component on its own; with
    fewer colons, or unequal counts, the two agree on all 483,997.
  - The comparison dates from ff18f84c70f (2016-12-12, "rgw: Added a
    globbing method for AWS Policies"). ARN conditions reach it since
    62c3e5ec69f (2025-03-13, "rgw/iam: add policy evaluation for
    Arn-based Conditions"), and main still has it at dffaf990666.
- **Impact:** a Deny whose ARN pattern has a wildcard component can fail to
  apply. `ArnNotLike` and `ArnNotEquals` negate the same wrong answer, so a
  statement using them can apply where its author meant it not to.
- **Releases:** every release that evaluates ARN conditions: v19.2.3 and
  later squid, every tentacle release from v20.1.0, and v18.2.8 and later
  reef.
- **rgw-go:** does not reproduce it. Its ARN conditions match each
  component on its own with `MatchWildcards` (`policy.MatchPolicy`), and
  `docs/exclusions.md` records the difference.
- **Upstream:** none known. Searched 2026-09-30; each search was first run
  on a known match:
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds the QA runs
    listing ceph/ceph#57907 by `match_wildcards`, a word only in their
    descriptions. Terms: `match_policy`, `arn_like`, `ArnLike`,
    `ArnEquals`, `ArnNotLike`, `MATCH_POLICY_ARN` and `aws:SourceArn`. The
    only ARN-condition issue is
    [#70481](https://tracker.ceph.com/issues/70481), "iam policy parses
    ArnLike/ArnEquals conditions but evaluates them to false", with its
    reef and squid backports #70595 and #70596; its fix routed ARN
    conditions through `match_policy`. Nothing reports the comparison.
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#57907 by `match_wildcards`. Terms: `match_policy`
    (ceph/ceph#53156 and #16491, both on `match_wildcards`, and an
    unrelated logging change), `arn_like` (none) and `ArnLike`
    ([ceph/ceph#62285](https://github.com/ceph/ceph/pull/62285), the
    #70481 fix, and #62284, bucket logging). None fixes it.
- **Found:** phase 1 authorization work (unit Z), 2026-09-29, transcribing
  `match_policy` for rgw-go's policy matching.

## radosgw takes any policy Action starting with a wildcard for every action

- **Kind:** defect, unfixed through main.
- **Evidence:**
  - The policy parser, `ParseState::do_string`, tests only the first
    character of an Action or NotAction string (`*s == '*'`): when it is
    `*`, the statement gets every action (`allValue`), and only other
    strings are matched against the action names (`rgw_iam_policy.cc:632`
    at v19.2.6, `:645` at v20.2.4). So `"*:GetObject"` or `"*Object"` means
    every action, where matching it as a pattern would give only the
    actions it names.
  - A `Principal` or `NotPrincipal` given as a string is tested the same
    way (`:625` and `:627` at v19.2.6, `:638` and `:640` at v20.2.4), so
    `"Principal": "*x"` is `Principal::wildcard()`, every principal,
    anonymous included.
  - Such a policy is accepted as valid. The same file already compares the
    full token in `parse_principal_` (main `:369`).
- **Impact:** an Allow statement whose Action starts with `*` grants every
  action, not only those its pattern names, a privilege escalation; a Deny
  statement denies every action. A Principal that starts with `*` names
  everyone.
- **Releases:** v19.2.6, v20.2.4 and main (7f50f7a552c, 2026-09-30,
  `rgw_iam_policy.cc:784`, `:786` and `:791`); older releases not checked.
- **rgw-go:** not decided yet: its policy parser is not written.
  `policy.MatchAction` notes that radosgw's parser takes such a pattern for
  `*` before any matching.
- **Upstream:** [#81229](https://tracker.ceph.com/issues/81229), which we
  filed. Its fix, [ceph/ceph#72262](https://github.com/ceph/ceph/pull/72262),
  a draft, compares the full token, `std::string_view{s, l} == "*"`, at the
  three sites. No earlier issue or pull request reports it (searched
  2026-09-30); each search method was first run on a known match.
  - tracker.ceph.com: every project's issues of every status through the
    issue filter "any searchable field contains", which finds #80991 by
    `rgw_usage_max_shards`. Terms: `is_valid_action`, `allValue` and `*:Get`
    (none), `wildcard action` (#68040, on removing a deny-all bucket policy,
    and #62292, a Resource wildcard fixed by
    [ceph/ceph#53156](https://github.com/ceph/ceph/pull/53156)) and
    `NotAction` (#73983, NotAction not evaluated, and #68029, a null
    dereference in `rgw_iam_policy.cc`).
  - ceph/ceph pull requests through `gh search prs`, which finds
    ceph/ceph#72160 by `rgw_usage_max_shards`. Terms: `is_valid_action`
    (#46824, spelling fixes), `allValue` (#70533, policy-evaluation
    logging) and `policy action wildcard` (eleven pull requests; the only
    change to action parsing, #20629, stops the action loop at its first
    match). None fixes it.
- **Found:** phase 1 authorization work (unit Z), 2026-09-30, writing
  rgw-go's action matching. A C++17 reproducer of `do_string`'s exact
  predicate, re-run independently, is attached to #81229; no policy was run
  end to end through `rgw::IAM::Policy`, which needs a full build. Not
  reproduced on a running radosgw.
