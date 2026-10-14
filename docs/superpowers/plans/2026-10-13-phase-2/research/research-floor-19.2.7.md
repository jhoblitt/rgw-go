# Research: moving the Squid floor from v19.2.6 to v19.2.7

The owner approved the move on 2026-10-13. Tentacle stays at v20.2.4. The compatibility rule is
spec §8: RADOS state in both directions, not radosgw's defects.

## 1. Scope

- **The move.** The Squid floor goes from v19.2.6 to v19.2.7 (9ec952b9f96, tagged 2026-10-08).
  v19.2.6 is an ancestor of v19.2.7 (`git log v19.2.7..v19.2.6` is empty), so the difference is
  only what the squid branch gained: a742f50616e plus the version commit. Tentacle stays at v20.2.4.
- **What changed.** 36 non-merge commits touch `src/rgw` between the tags, none `src/cls`. Of the
  option files only `rgw.yaml.in` changes: one `startup` flag and two lines of help text. The
  brief's 14 commits are analysed in section 2, along with 69c327f161c and 05c4a49d28d. The beast,
  shutdown, finisher, notify, http-client, datalog and neorados commits change no behaviour a
  client or RADOS sees (section 2, "Internals").
- **The rule applied** is spec §8's compatibility rule: RADOS state in both directions, the correct
  S3 behaviour where radosgw's is wrong, corruption prevented even where radosgw allows it, and
  every difference recorded as an exclusion.
- **Verdicts at a glance:**

| Commit | Area | v20.2.4 already? | Verdict |
|---|---|---|---|
| f6877f01fe4 | GC of an overwritten or deleted object's tails | yes | no change |
| ce98cebb815 (+c5ed95d1d24) | PUT and DELETE conditions, DeleteObjects | yes, MFA inversion included | registry, exclusions and one live test fixture; phase 2 input |
| 1db3ab3b185, eb63ba5d7de | SigV4 signed-header checks | **no**: v20.2.4 still refuses | **behaviour change** (auth) |
| 148e1d182a0, 7b8039bb2a8 | radosgw's outgoing signer | yes | no change (rgw-go signs nothing outgoing) |
| 596eaf34725 | guard_reshard retries ENOENT | **no** (tentacle branch only) | **behaviour change** (driver) |
| 4b567265365 | ETag of a re-sent CompleteMultipartUpload | yes | **behaviour change** (driver, memstore) |
| 8001a4d4fd8 | copy_obj_data accounted size | no, but unreachable on Squid | no change |
| 41984610868 | DeleteObjects per-key error code | yes | no change |
| 4dac1850993 | x-amz-expiration (phase 2) | **no** (tentacle branch only) | registry entry; phase 2 input |
| 3f9eb56d5ec | obj_lock in the bucket instance JSON (phase 2) | **no** (tentacle branch only) | **behaviour change** (meta, and the driver's put) |
| b9b65194f64 | transition of an encrypted multipart object (phase 2) | yes, from v20.2.2 | registry entry; phase 2 input |
| 42fe9817449 | cloud-tier multipart resume (phase 2) | n/a | out of scope: cloud tier is excluded |
| 88c57354a9f | rgw_gc_max_objs startup-only | yes | registry note only |

- **Counted as behaviour changes:** five, in four areas. Squid needs each of them, because v19.2.7
  behaves the new way. Four are also new on Tentacle, where v20.2.4 does not have the fix (section
  6, Q1): SigV4, the reshard retry, and both halves of obj_lock. One, the put half of obj_lock,
  can wait for phase 2's object-lock unit.
- **What the phase 2 research reports must re-check.** The reports in this directory were written
  against v19.2.6 as the Squid floor:
  - `research-versioning.md` names the changed write-condition, delete, MFA and `guard_reshard`
    code at 59 lines. For example, its floor-defect row 3, a versioned PUT's conditions evaluated
    against the new instance, is fixed on Squid by v19.2.7's `get_current_version_state`, so its
    exclusion and known difference go away.
  - `research-lock-lifecycle.md` names x-amz-expiration, `obj_lock` and `transition_obj` at 24
    lines. Its D6 says the x-amz-expiration fix is "in no release", which is no longer true for
    Squid. Its transition section does not cover the encrypted-multipart part lengths of C13.
  - `research-writepath.md` names `transition_obj`, `copy_obj_data` and
    `check_previously_completed` at 13 lines.
  - `research-bucketconfig.md` names none of the changed code.

## 2. Per-commit analysis

Each commit gets six answers: (1) what changed, (2) v20.2.4, (3) rgw-go at origin/main (0239625),
(4) the verdict, (5) the parity outcomes it may flip when radosgw's Squid ledgers are re-recorded,
and (6) the registry and exclusions entries it touches. "The registry" is
`docs/ceph-upstream-bugs.md`. A line number cited without a file is in that file at origin/main.

### C1 f6877f01fe4: enqueueing the overwritten object for GC

1. ce98cebb815 (C2) stopped calling `prepare_atomic_modification` from the delete path. That call
   was what loaded the `state` and `manifest` members that `complete_atomic_modification` reads.
   f6877f01fe4, applied after it, repairs that:
   - it moves a `get_state` into `complete_atomic_modification`
     (`src/rgw/driver/rados/rgw_rados.cc:5455-5459@v19.2.7`, absent at
     `src/rgw/driver/rados/rgw_rados.cc:5382-5385@v19.2.6`);
   - `_do_write_meta` now sets the members from the state it read
     (`src/rgw/driver/rados/rgw_rados.cc:3185-3186@v19.2.7`).

   Net effect between the tags: none. The tails queued are still those of the head as read before
   the op, under its tail tag or its object tag.
2. v20.2.4 is the same (`src/rgw/driver/rados/rgw_rados.cc:6102-6106@v20.2.4`).
3. rgw-go queues the tails of the head that the write or the delete read
   (internal/driver/headwrite.go:284-286, internal/driver/headwrite.go:351-378).
4. No change.
5. None.
6. None.

### C2 ce98cebb815: conditional PUT, DeleteObject and DeleteObjects (c5ed95d1d24 adds debug logs only)

1. **Write path.** `_do_write_meta` now checks If-Match and If-None-Match through
   `check_preconditions` before `prepare_atomic_modification`
   (`src/rgw/driver/rados/rgw_rados.cc:3185-3205@v19.2.7`). Before, they were checked inside
   `need_guard` (`src/rgw/driver/rados/rgw_rados.cc:3171@v19.2.6`,
   `src/rgw/driver/rados/rgw_rados.cc:6489-6544@v19.2.6#prepare_atomic_modification`). A Squid
   client sees these changes:
   - a quoted condition is unquoted;
   - If-Match, `*` or an ETag, on a missing key is 404, not 412;
   - an If-None-Match ETag passes on a missing key;
   - a head with a fake tag is checked too;
   - a versioned instance's conditions are judged against the current version, and its write
     sends no idtag guard (`set_attr_id_tag`). The checks are at
     `src/rgw/driver/rados/rgw_rados.cc:6673-6712@v19.2.7`.

   **DeleteObject** reads If-Match, `x-amz-if-match-size` and `x-amz-if-match-last-modified-time`
   (`src/rgw/rgw_rest_s3.cc:3435-3500@v19.2.7#RGWDeleteObj_ObjStore_S3::get_params`). `delete_obj`
   checks them with `check_preconditions` and an `obj_check_mtime` EQ in the removal op
   (`src/rgw/driver/rados/rgw_rados.cc:6049-6057@v19.2.7`). On a versioned bucket it checks the
   current version, or the instance's dirent
   (`src/rgw/driver/rados/rgw_rados.cc:5882-5897@v19.2.7`,
   `src/rgw/driver/rados/rgw_rados.cc:5913-5942@v19.2.7`). The head's removal no longer carries the
   idtag `cmpxattr` that v19.2.6 sent through `prepare_atomic_modification(removal_op)`
   (`src/rgw/driver/rados/rgw_rados.cc:5914@v19.2.6`). The only caller of
   `prepare_atomic_modification` left is the write path
   (`src/rgw/driver/rados/rgw_rados.cc:3203@v19.2.7`).

   **DeleteObjects** parses ETag, LastModifiedTime and Size for each Object
   (`src/rgw/rgw_multi_del.cc:17@v19.2.7#RGWMultiDelObject::xml_end`). Its MFA check is inverted:
   it asks for MFA when a key names no version
   (`src/rgw/rgw_op.cc:7067-7080@v19.2.7`, against `src/rgw/rgw_op.cc:7055-7068@v19.2.6`). The rest
   of `RGWDeleteMultiObj::execute` is unchanged.

   **Not changed.** CompleteMultipartUpload still reads neither condition
   (`src/rgw/rgw_rest_s3.cc:4094-4104@v19.2.7#RGWCompleteMultipart_ObjStore_S3::get_params`).
   UploadPart still passes none to the part head, since `rgw_putobj_processor.cc` did not change.
2. v20.2.4 behaves the v19.2.7 way here:
   - `check_preconditions` and `get_current_version_state` are byte-identical between
     `src/rgw/driver/rados/rgw_rados.cc:6624-6780@v19.2.7` and
     `src/rgw/driver/rados/rgw_rados.cc:7238-7394@v20.2.4`;
   - the `_do_write_meta` block is the same at `src/rgw/driver/rados/rgw_rados.cc:3282-3302@v20.2.4`;
   - DeleteObject's `get_params` is identical (`src/rgw/rgw_rest_s3.cc:3685-3750@v20.2.4`), and so
     is `rgw_multi_del.cc` with its header;
   - `delete_obj` differs only in datalog error handling and the op move;
   - the MFA inversion is the same (`src/rgw/rgw_op.cc:7969-7983@v20.2.4`).

   What still differs is Tentacle's own: its CompleteMultipartUpload takes both conditions
   (`src/rgw/rgw_rest_s3.cc:4565@v20.2.4#RGWCompleteMultipart_ObjStore_S3::get_params`), and its
   UploadPart passes them to the part head.
3. rgw-go:
   - PUT conditions are v20.2.4's on both releases (internal/driver/headwrite.go:110-145).
   - DeleteObject and DeleteObjects conditions are v20.2.4's on both releases, and a delete keeps
     v19.2.6's idtag guard on both (docs/exclusions.md:4257-4296).
   - The MFA check is main's corrected one on both (internal/op/deleteobjects.go:181-187).
   - `guardHead` drops the guard for a versioned instance on Tentacle only
     (internal/driver/headwrite.go:342-344). On Squid it guards as v19.2.6 did. Phase 1 never
     reaches that branch, because it refuses versioned and object-lock buckets
     (internal/op/deleteobjects.go:160-162, internal/driver/mp_complete.go:270-273).
4. **No behaviour change.** What does change:
   - the registry and the exclusions (item 6);
   - one test fixture: test/integration/write_test.go:273-291 asserts v19.2.6's three answers (412,
     412, 412) against the live Squid radosgw. At v19.2.7 radosgw answers v20.2.4's (404, 200, 200),
     so the Squid branch collapses into the Tentacle one. That edit belongs in the pin-bump PR
     (F11): the new expectations fail against v19.2.6, and the old ones fail against v19.2.7.
   - Phase 2 input: the split in `guardHead` becomes moot, since both floors skip the guard for a
     versioned instance. Phase 2's versioning unit either drops the split or keeps the stricter
     guard on both releases. Without the guard, only a race between two writers of one existing
     instance is exposed, and the cost is a leaked tail.
5. In radosgw's Squid s3-tests ledger, these eight tests should go from failed to passed, as
   v20.2.4 passes them:
   - test_put_object_if_match and test_put_object_ifmatch_nonexisted_failed;
   - test_delete_object_if_match, test_delete_object_if_match_size and
     test_delete_object_if_match_last_modified_time;
   - test_delete_objects_if_match, test_delete_objects_if_match_size and
     test_delete_objects_if_match_last_modified_time.

   test_multipart_put_object_if_match should stay failed. `test/s3tests/known-differences-squid.txt`
   then loses the eight lines (a known difference that agrees fails the comparison as stale) and
   keeps test_multipart_put_object_if_match.
6. Registry:
   - "Squid's PUT refuses a quoted If-Match that matches the object's ETag" (:6321): the quoted
     rejection and the 412 on a missing key are fixed in v19.2.7. The prefix comparison is unfixed
     at v19.2.7 too.
   - "Squid's write conditions fail an If-None-Match ETag on a missing key and skip a head without
     a write tag" (:6390): fixed in v19.2.7.
   - "Tentacle's DeleteObject removes the head without checking its write tag" (:7067): v19.2.7
     carries it.
   - "Tentacle's DeleteObjects asks for MFA ..." (:8557): v19.2.7 carries it, so its "No v19.2.x
     tag contains ce98cebb815" is now false.
   - "Tentacle fails an UploadPart that sends If-None-Match: * ..." (:7231): Squid still ignores
     the conditions at v19.2.7.

   Exclusions:
   - "Write conditions are Tentacle's on Squid too" (docs/exclusions.md:4225-4256) shrinks to its
     CompleteMultipartUpload bullet.
   - "Delete conditions are Tentacle's, and a delete is guarded, on both releases"
     (docs/exclusions.md:4257-4296) loses its two Squid-zone bullets, and its guard bullet covers
     both releases.
   - "Deletes that need MFA are refused" (docs/exclusions.md:3610-3627): the inversion is on both
     releases.
   - "UploadPart ignores If-Match and If-None-Match" (docs/exclusions.md:4673): its Squid side is
     v19.2.7's.

### C3 1db3ab3b185 and C4 eb63ba5d7de: SigV4's signed-header checks

1. Unless `rgw_sigv4_insecure` is set, v19.2.7 no longer requires a present Content-Type to be
   signed. It also skips `x-amz-content-sha256` in its scan for unsigned `x-amz-*` headers. `host`
   and every other `x-amz-*` header must still be signed (`src/rgw/rgw_auth_s3.cc:788-842@v19.2.7`;
   the Content-Type refusal was at `src/rgw/rgw_auth_s3.cc:798-802@v19.2.6`). Leaving
   x-amz-content-sha256 unsigned is safe, because its value enters the canonical request as
   HashedPayload either way (`src/rgw/rgw_auth_s3.h:638-660@v19.2.7#get_v4_exp_payload_hash`, used
   at `src/rgw/rgw_rest_s3.cc:5858@v19.2.7`).
2. **v20.2.4 does not have either change**: it refuses both (`src/rgw/rgw_auth_s3.cc:774-796@v20.2.4`).
   Here Squid is ahead of Tentacle. The tentacle branch has the Content-Type fix (7e2e2072822, per
   the registry). It does not have the x-amz-content-sha256 fix as of ab83d026c29 (2026-10-12).
3. rgw-go refuses both on every release (internal/auth/sigv4.go:305-318). `auth.Config` carries no
   release (internal/auth/config.go:13-38). The vector
   internal/auth/testdata/v4/unsigned-content-type-rejected.json pins the refusal.
4. **Behaviour change (auth, F4).** Squid requires it, because v19.2.7 serves such requests. On
   Tentacle, the compatibility rule's correct S3 behaviour is to accept them as well. That is
   recommended, with an exclusion, but it relaxes a check that radosgw v20.2.4 enforces (Q1).
5. These tests should go from failed to passed in the Squid ledger:
   test_sigv4_contenttype_unsigned, test_sigv4_presigned_contenttype_unsigned and
   test_sigv4_without_signed_content_sha256. Tentacle's radosgw keeps failing them, so if F4
   applies on Tentacle they go into `known-differences-tentacle.txt`.
6. Registry:
   - "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned" (:927, index
     row :41): fixed in v19.2.7. Its "rgw-go:" line follows F4.
   - New entry: "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose x-amz-content-sha256 is
     unsigned". It is S3-correctness and fails closed; unreproduced; fixed in v19.2.7; upstream
     trackers #79803 and #79873, fixed by ceph/ceph#71292 on main and ceph/ceph#71360 on squid.

   Exclusions: a new bullet for Tentacle if F4 covers it.

### C5 148e1d182a0 and C6 7b8039bb2a8: radosgw's outgoing AWSSignerV4

1. radosgw's own signer always sends x-amz-content-sha256: UNSIGNED-PAYLOAD, or a forwarded
   request's own value (`src/rgw/rgw_rest_s3.cc:5664@v19.2.7#AWSSignerV4::prepare`).
2. v20.2.4 has both changes (`src/rgw/rgw_rest_s3.cc:6212-6227@v20.2.4`).
3. rgw-go signs no outgoing request. It neither forwards a request nor receives a forwarded one
   (docs/exclusions.md:3165-3166). Phase 2 transitions only within the cluster (spec §9,
   docs/superpowers/specs/2026-09-25-rgw-go-design.md:363-371).
4. No change. If a later phase signs outgoing requests (cloud tier), it should follow both
   floors' signer.
5. None. 6. None.

### C7 596eaf34725: guard_reshard retries an ENOENT

1. Both reshard guards now retry an `-ENOENT`, within NUM_RESHARD_RETRIES (10):
   - `UpdateIndex::guard_reshard`, after `invalidate_bs()`
     (`src/rgw/driver/rados/rgw_rados.cc:7225-7230@v19.2.7`; the bound at
     `src/rgw/driver/rados/rgw_rados.cc:7216@v19.2.7`);
   - `RGWRados::guard_reshard` (`src/rgw/driver/rados/rgw_rados.cc:7929-7933@v19.2.7`).

   The retry re-reads the bucket. `init_bs` passes `&target->bucket_info`
   (`src/rgw/driver/rados/rgw_rados.h:947-955@v19.2.7`), and that `BucketShard::init` overload
   always reads the instance into it (`src/rgw/driver/rados/rgw_rados.cc:2804-2830@v19.2.7`). The
   retry covers four groups of call:
   - the prepare (`src/rgw/driver/rados/rgw_rados.cc:7289-7291@v19.2.7`);
   - the cancel (`src/rgw/driver/rados/rgw_rados.cc:7395-7397@v19.2.7`);
   - the completion manager's retry (`src/rgw/driver/rados/rgw_rados.cc:905@v19.2.7`);
   - the OLH ops (`src/rgw/driver/rados/rgw_rados.cc:8153@v19.2.7`,
     `src/rgw/driver/rados/rgw_rados.cc:8207@v19.2.7`,
     `src/rgw/driver/rados/rgw_rados.cc:8349@v19.2.7`,
     `src/rgw/driver/rados/rgw_rados.cc:8380@v19.2.7`).

   A write whose bucket was resharded after the request read it no longer fails 404 when it
   prepares after the old shards are gone.
2. **v20.2.4 does not have it**: there `UpdateIndex::guard_reshard` breaks on any answer other
   than busy-resharding (`src/rgw/driver/rados/rgw_rados.cc:7888-7891@v20.2.4`). The tentacle
   branch has it, as 183829b915c (after v20.2.4, in no tag). Upstream it is e34067f8cea, merged by
   ceph/ceph#66553 and first tagged in v21.0.0; the squid branch took it through ceph/ceph#70498.
3. rgw-go's `indexOp.guardReshard` retries only ErrBusyResharding
   (internal/driver/indexop.go:227-252). The registry says rgw-go reproduces the 404
   (:11807-11810), and test/integration/write_test.go:403-412 tolerates one stale-shard 404 by
   re-sending.
4. **Behaviour change (driver, F7).** Squid requires it. On Tentacle it is the correct behaviour,
   with an exclusion (Q1). rgw-go can do better than radosgw's blind loop: it re-reads the instance
   from RADOS and retries only when the instance now names another shard object.
5. No s3-test or admin-suite test exercises it. The integration spec's tolerance goes.
6. Registry: "radosgw fails a write whose bucket was resharded since the request read it" (:11770,
   index row :257) gets "fixed in v19.2.7 by 596eaf34725". Its "Upstream: pending" line gains the
   fix. Exclusions: "Dynamic resharding worker" (docs/exclusions.md:262-318) gets the ENOENT retry.

### C8 4b567265365: ETag of a re-sent CompleteMultipartUpload

1. `check_previously_completed` sets `etag = oetag` (`src/rgw/rgw_op.cc:6584@v19.2.7`), so a
   retried completion of a finished upload answers with the object's ETag, not `""`. This is
   tracker #75999.
2. v20.2.4 has the same assignment (`src/rgw/rgw_op.cc:7472@v20.2.4`).
3. rgw-go reproduces each release:
   - the driver sets the ETag on Tentacle only (internal/driver/mp_complete.go:286-293);
   - the memstore clears it below Tentacle (internal/memstore/multipart.go:267-270).

   Three specs pin the Squid behaviour: internal/driver/mp_complete_test.go:461-478,
   internal/s3/multipart_test.go:889-913 and the memstore's internal/memstore/multipart_test.go:347.
4. **Behaviour change (driver F5, memstore F6)**: answer the stored ETag on both releases.
5. None: no ledger test re-sends a completion.
6. Registry: "Squid answers a re-sent CompleteMultipartUpload of a finished upload with an empty
   ETag" (:9103) gets "fixed in v19.2.7". Its "rgw-go:" line follows F5 and F6.

### C9 8001a4d4fd8: copy_obj_data's accounted size for a compressed source

1. When the destination is not compressed, the source's accounted size is used, not `ofs`
   (`src/rgw/driver/rados/rgw_rados.cc:5145-5146@v19.2.7`; `ofs` at
   `src/rgw/driver/rados/rgw_rados.cc:5108@v19.2.6`). On Squid this cannot change an outcome:
   - `copy_obj` refuses an encrypted source
     (`src/rgw/driver/rados/rgw_rados.cc:4787-4795@v19.2.7`);
   - it always copies the source's compression attr into the destination's attrs
     (`src/rgw/driver/rados/rgw_rados.cc:4823-4825@v19.2.7`).

   So a compressed source still reads as compressed, and for an uncompressed one `ofs` is its size.
2. v20.2.4 still uses `ofs` (`src/rgw/driver/rados/rgw_rados.cc:5378@v20.2.4`). That matters only
   on Tentacle's own paths, where the destination gets a compression or encryption filter.
3. rgw-go takes the accounted size from the compression info it keeps
   (internal/driver/copy.go:424-450) and refuses an encrypted source
   (internal/driver/copy.go:127-135).
4. No change.
5. None. 6. None.

### C10 41984610868: a local error code in handle_individual_object

1. Each key's result now comes from a local error code, not from the shared `op_ret` that
   concurrent coroutines overwrote (`src/rgw/rgw_op.cc:6879@v19.2.6` and
   `src/rgw/rgw_op.cc:6894@v19.2.6`, against `src/rgw/rgw_op.cc:6888@v19.2.7` and
   `src/rgw/rgw_op.cc:6906@v19.2.7`; tracker #76960).
2. v20.2.4 uses a local too (`src/rgw/rgw_op.cc:7816@v20.2.4`, `src/rgw/rgw_op.cc:7834@v20.2.4`).
3. rgw-go computes each key's result on its own (internal/op/deleteobjects.go:229-248).
4. No change.
5. A DeleteObjects test whose Squid outcome raced could settle. Explain such a flip with this
   commit.
6. No entry exists. Add one, "Squid's DeleteObjects reports another key's result" (S3-correctness,
   v19.2.6 only, fixed in v19.2.7), only if the re-record shows a flip this explains.

### C11 4dac1850993: x-amz-expiration for the current version only (phase 2)

1. A request naming a version gets no header, and the header is computed from the Expiration rule
   alone (`src/rgw/rgw_lc.cc:2766@v19.2.7`, `src/rgw/rgw_lc.cc:2850@v19.2.7`, in
   `src/rgw/rgw_lc.cc:2750@v19.2.7#s3_expiration_header`). The REST helper passes the client's key
   and versionId, not the OLH-resolved key. Tracker #77564.
2. **v20.2.4 does not have it**: it takes the noncurrent rule for a key with an instance
   (`src/rgw/rgw_lc.cc:2916@v20.2.4`, as `src/rgw/rgw_lc.cc:2849@v19.2.6`). The tentacle branch has
   it, as 30daa120099.
3. rgw-go sends no x-amz-expiration until phase 2 (docs/exclusions.md:4023-4028).
4. No change now. Phase 2's lifecycle unit computes the header in v19.2.7's form on both releases,
   which research-lock-lifecycle.md D6 already recommends. On Tentacle that is an exclusion (Q1).
5. None in the phase 1 set.
6. New registry entry: "radosgw's x-amz-expiration reports a NoncurrentVersionExpiration rule"
   (S3-correctness; unreproduced; v19.2.6 and v20.2.4; fixed in v19.2.7).

### C12 3f9eb56d5ec: obj_lock in the bucket instance JSON (phase 2)

1. `RGWBucketInfo::dump` writes `obj_lock` when object lock is enabled, and `decode_json` reads it
   (`src/rgw/rgw_common.cc:2542-2544@v19.2.7`, `src/rgw/rgw_common.cc:2588-2590@v19.2.7`).
   RGWObjectLock's JSON form changes too:
   - `enabled` is written only when true;
   - `rule` holds `defaultRetention` (it was `default_retention`);
   - every member goes through encode_json.

   The dumps are at `src/rgw/rgw_object_lock.cc:26-33@v19.2.7#DefaultRetention::dump`,
   `src/rgw/rgw_object_lock.cc:56-58@v19.2.7#ObjectLockRule::dump` and
   `src/rgw/rgw_object_lock.cc:93-101@v19.2.7#RGWObjectLock::dump`. The decoders start at
   `src/rgw/rgw_object_lock.cc:8@v19.2.7#DefaultRetention::decode_json` and
   `src/rgw/rgw_object_lock.cc:65@v19.2.7#RGWObjectLock::decode_json`. A missing non-mandatory
   member decodes as `T()` (`src/common/ceph_json.h:334-346@v19.2.7`), so a document without
   `obj_lock` still resets the rule to RGWObjectLock(): enabled, with no rule.
2. **v20.2.4 has neither**: it keeps only the binary encode
   (`src/rgw/rgw_common.cc:2363-2364@v20.2.4`) and the old form
   (`src/rgw/rgw_object_lock.cc:77@v20.2.4#RGWObjectLock::dump`). The tentacle branch has the fix
   (ceph/ceph#70500, in no tag).
3. rgw-go:
   - its dump omits `obj_lock` ("Squid and Tentacle write it; main adds obj_lock",
     internal/meta/bucket_info.go:326-385);
   - it holds the lock configuration opaque (internal/meta/bucket_info.go:77-80);
   - its put keeps a stored instance's configuration and gives a new instance the default
     (internal/driver/metadata.go:592-598, internal/driver/metadata.go:628).
4. **Behaviour change, in two parts:**
   - (a) meta, F8: the instance's JSON carries `obj_lock` in v19.2.7's form. Omitting it is a
     hazard: a get from rgw-go and a put into a v19.2.7 radosgw resets the rule.
   - (b) driver, F9, which can wait for phase 2: a put takes a document's `obj_lock` when the
     flags enable object lock and the document carries one, and keeps the stored one when the
     document has none. The second half is stricter than v19.2.7, which resets it.

   Squid needs both. Tentacle is Q1.
5. None: no populated bucket has object lock, and no gate, s3-test or admin-suite test reads an
   object-lock instance's JSON. No corpus RGWBucketInfo golden has BUCKET_OBJ_LOCK_ENABLED (their
   flags are 0, 1 and 2), and no encode changed, so regenerating the goldens at v19.2.7 changes
   nothing.
6. Registry: "radosgw's bucket instance metadata put drops the bucket's object lock rule" (:11384)
   already says fixed in v19.2.7. Add the reset-when-absent residual; its "rgw-go:" line follows
   F8 and F9. Exclusions: in "The metadata sections write only what keeps a user or bucket whole",
   "which neither release's JSON carries" (docs/exclusions.md:1550-1554) is false at v19.2.7.

### C13 b9b65194f64 and 42fe9817449: transition of an encrypted multipart object; cloud-tier resume (phase 2)

1. Before rewriting the manifest, `transition_obj` records an encrypted source's part lengths in
   RGW_ATTR_CRYPT_PARTS (`src/rgw/driver/rados/rgw_rados.cc:5155-5182@v19.2.7#fixup_manifest_to_parts_len`,
   called at `src/rgw/driver/rados/rgw_rados.cc:5211@v19.2.7`). v19.2.6's `transition_obj`
   (`src/rgw/driver/rados/rgw_rados.cc:5117@v19.2.6`) does not. Without the part lengths, a
   transitioned encrypted multipart object cannot be decrypted.

   42fe9817449 touches only `rgw_lc_tier.cc`, the cloud tier, which is out of scope.
2. v20.2.4 has it (`src/rgw/driver/rados/rgw_rados.cc:5388@v20.2.4`,
   `src/rgw/driver/rados/rgw_rados.cc:5444@v20.2.4`), first in v20.2.2. v20.2.0 and v20.2.1 lack it.
3. rgw-go has no transition yet, and it refuses an encrypted copy source
   (internal/driver/copy.go:127-135).
4. No change now. Phase 2 input: its in-cluster transition must write the part lengths as
   `fixup_manifest_to_parts_len` does, or skip encrypted objects. Objects that v19.2.6 transitioned
   without them are damage rgw-go does not decrypt and cannot repair.
5. None.
6. New registry entry: "Squid's lifecycle transition of an encrypted multipart object drops its
   part lengths". It is data corruption; unreproduced; affects v19.2.6, v20.2.0 and v20.2.1; fixed
   in v19.2.7 and v20.2.2; tracker #23264.

### C14 88c57354a9f: rgw_gc_max_objs becomes startup-only

1. The option gains the `startup` flag (`src/common/options/rgw.yaml.in:1713-1714@v19.2.7`), and
   the GC sizes its I/O manager from the count it started with.
2. v20.2.4 has the same flag (`src/common/options/rgw.yaml.in:1795-1796@v20.2.4`).
3. rgw-go reads the option once, at Open (internal/driver/writer.go:97-103).
4. No change.
5. None.
6. Registry: in "radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards"
   (:550), the "related" runtime-change crash (:615-616) is now kept from happening on both floors
   by the startup flag. The commit names no tracker, so #75958 is not asserted closed.

### Internals

These change no behaviour a client or RADOS sees:
- RGWSI_Notify now starts only with the cache (`src/rgw/driver/rados/rgw_service.cc:73-75@v19.2.7`),
  as at `src/rgw/driver/rados/rgw_service.cc:66-68@v20.2.4`. The control object names do not change.
- 69c327f161c catches a send failure in `abort_early`, a crash fix.
- 05c4a49d28d stops an admin AssumeRole of a missing role from crashing. That is STS, phase 3.
- The beast, shutdown, finisher, http-client, datalog and neorados commits are internal to the
  process.

### Registry entries v19.2.7 still carries

These entries are unchanged in substance, because their code did not change between the tags.
Each Releases line gains v19.2.7; the line numbers are their Releases lines.
- "Squid does not guard listing-time index suggestions against resharding": :292.
- "radosgw's notification queue listing never pages past 1024 queues": :1659.
- "Squid's realm reload hangs ...": :1712-1713.
- "Squid's negated condition operators ...": :4609-4611.
- "Squid's is_public counts ...": :5975-5977.
- "radosgw's gc stalls a converted shard ...": :7557-7560.
- "Squid labels a copy to another storage class with its source's class": :7997.
- "radosgw's CompleteMultipartUpload does not keep its lock past rgw_mp_lock_max_time": :9003.
- "radosgw stores request credentials as object attrs": :10145-10146.

Several "rgw-go:" lines in these entries reproduce Squid's rule, for example the policy semantics
at :4612-4614 and :5978-5980. They stay right for v19.2.7. Whether they should keep reproducing it
under the 2026-10-13 rule is the separate compatibility sweep, not part of this move.

## 3. Operational changes

These are all at origin/main. Code comments that cite "at v19.2.6" as provenance stay and are not
listed.

**The cluster pin and its checks**
- `hack/rooket/squid/values/rook-ceph-cluster.yaml:5`: `tag: v19.2.6` becomes `v19.2.7`. This file
  is the one home of Squid's image.
- `hack/rooket/lib.sh`: no code change. `pinned_version` derives the version from the tag
  (hack/rooket/lib.sh:61-69). `up.sh` checks every pod runs it (hack/rooket/up.sh:19-20), and
  `populate.sh` writes it into `manifest.json` (hack/rooket/populate.sh:21), which every parity
  record reads. The comment example at :62 ("v19.2.6 is 19.2.6") can stay.
- `.goreleaser.yaml:13`: `SQUID_CEPH_TAG=v19.2.6` becomes `v19.2.7`, in the same commit as the
  values file. `hack/image/release-pins-check.sh:34-42` fails when the two disagree, and the
  derived image and cgo build read the tag (hack/image/build.sh:26-28,
  hack/image/cgo-build.sh:31-33).
- Unverified: that `quay.io/ceph/ceph:v19.2.7` is published. The sandbox reaches neither quay.io
  nor download.ceph.com. F11 checks it first.

**CI**
- `.github/actions/rooket-cluster/action.yml:48-50`: the comment names Squid 19.2.6 as a release
  creating AES256KRB5 keys, and says it has no noble build. It becomes 19.2.7. Whether 19.2.7 has a
  noble build is unverified. Nothing functional changes either way, because the step installs
  librados from `debian-20.2.4` for both clusters (:60).
- No workflow hardcodes the version. `.github/workflows/integration.yml:85` and the Makefile's
  parity targets read `ceph_version` from `manifest.json` (Makefile:240, :254, :270, :287).

**The Makefile**
- Makefile:219, the `parity-record` help text's example META `ceph_version=19.2.6`, becomes
  `19.2.7`.

**The spec and the support policy**
- docs/superpowers/specs/2026-09-25-rgw-go-design.md:57-59 (§3): "Squid 19.2.6 or Tentacle 20.2.4"
  and "runs 19.2.6 or later on Squid" become 19.2.7.
- The same file :501-512 (§12): the support policy becomes "librados 19.2.7 or later on Squid".
  Keep the technical fact, which does not move: AES256KRB5 keys parse from 19.2.6 and 20.2.4. The
  sentence "the cluster floor in section 3 is the same releases" stays true.
- docs/cgo-limitations.md:553-554 states the same support policy and changes the same way. Its
  benchmark table (:44-46) is a record.
- internal/radosclient/goceph/libversion.go:45-50 (`libradosFloors`, 19.2.6) and its message at
  :87-88 are the AES256KRB5 parse floor, a capability. They stay. rgw-go enforces no support floor
  at run time.
- hack/rooket/README.md:11 ("quay.io/ceph/ceph:v19.2.6 under Rook v1.20.7") becomes v19.2.7. :87 is
  the host's AES256KRB5 requirement and stays.

**Goldens**
- hack/goldens/gen.sh:5 and :15: the default `IMAGE` becomes `quay.io/ceph/ceph:v19.2.7`.
  Regenerating should change nothing. No encoding changed between the tags, the only dump change
  (RGWBucketInfo's `obj_lock`) needs BUCKET_OBJ_LOCK_ENABLED, which no corpus instance has, and
  RGWObjectLock is not in hack/goldens/types.txt. Regenerating needs podman and the image, not a
  cluster.

**Test fixtures that assert 19.2.6 as a live floor**
- test/integration/write_test.go:273-291: the Squid branch of "answers the write conditions as
  radosgw does" (C2). It changes in the pin-bump PR.
- None else asserts 19.2.6 as a live floor. These are records or fixtures, and stay:
  - the hack/parity specs (ledger_test.go, cli_test.go, differential_test.go, parity_test.go,
    basecheck*_test.go) and hack/parity/main.go's doc examples;
  - the hack/bench specs and testdata;
  - test/gate/manifest_test.go:16, which parses a fixture;
  - internal/radosclient/goceph/libversion_test.go, which pins the AES256KRB5 floor;
  - docs/benchmarks;
  - the phase 1 plans.
- The ledgers test/s3tests/baseline/squid.json and test/admin/baseline/squid.json are append-only.
  The re-record appends 19.2.7 entries and edits none.

**Parity mechanics that shape the order of the work**
- hack/parity's diff counts a baseline entry as valid only when its `ceph_version` is the
  candidate's (hack/parity/main.go:194-205). CI passes no `--allow-meta-drift` (Makefile:243,
  :257; .github/workflows/integration.yml:94). So the moment the Squid cluster runs 19.2.7, all 416
  s3-tests and 80 admin Squid entries read "baseline stale, re-record". **The pin bump and the
  re-record must share one PR.**
- A known-difference line whose test now agrees fails the comparison as stale
  (hack/parity/main.go:207-218). Each change to `known-differences-squid.txt` therefore lands with
  the change that makes its test agree: F4 for the SigV4 tests, F11 for the condition tests.
- The s3-tests pin, f6f2529dd9, is on the rgw-go-s3tests branch over ceph main
  (hack/s3tests/run.sh:26-31). The in-tree s3-tests changes that the squid PRs carried do not move
  it.

## 4. Registry and exclusions entries

Line numbers are docs/ceph-upstream-bugs.md and docs/exclusions.md at origin/main. F2 and F3 make
the fact edits, which are true of v19.2.7 whatever rgw-go does. Each behaviour task edits only the
"rgw-go:" line and the exclusion bullet of its own entry. Both files are shared with rgw-rs, so
every change goes to the rgw-rs session (CLAUDE.md).

### docs/ceph-upstream-bugs.md

**Fixed in v19.2.7**

| Entry (line) | Edit |
|---|---|
| radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned (:927; index :41) | Kind and Releases: fixed in v19.2.7 by eb63ba5d7de (squid ceph/ceph#71296, merge 18d35e32770). Tentacle is unfixed through v20.2.4; the tentacle branch carries it. F4 rewrites the "rgw-go:" line. The title stays true. |
| Squid's PUT refuses a quoted If-Match that matches the object's ETag (:6321) | The quoted rejection and the 412 on a missing key are fixed in v19.2.7 (ce98cebb815). The prefix comparison is unfixed at v19.2.7 as at v20.2.4. Upstream: ceph/ceph#65932 is first tagged in v19.2.7. |
| Squid's write conditions fail an If-None-Match ETag on a missing key and skip a head without a write tag (:6390) | Fixed in v19.2.7. Releases: v19.2.6 only. |
| Squid answers a re-sent CompleteMultipartUpload of a finished upload with an empty ETag (:9103) | Fixed in v19.2.7 (4b567265365). F5 and F6 rewrite the "rgw-go:" line. |
| radosgw fails a write whose bucket was resharded since the request read it (:11770; index :257) | Fixed in v19.2.7 by 596eaf34725 (squid ceph/ceph#70498; main e34067f8cea through ceph/ceph#66553, first tagged in v21.0.0; tentacle 183829b915c, in no tag). Releases: v19.2.6 and v20.2.4. Upstream: no longer "pending". F7 rewrites the "rgw-go:" line. |
| radosgw's bucket instance metadata put drops the bucket's object lock rule (:11384) | It already says fixed in v19.2.7. Add the residual: v19.2.7 still resets the rule for a document without `obj_lock`, since a missing member decodes as `T()`. F8 and F9 rewrite the "rgw-go:" line. |
| radosgw faults on a zero rgw_gc_max_objs, rgw_lc_max_objs or rgw_usage_max_shards (:550) | Upstream: the runtime-change crash is now kept from happening by the `startup` flag in v19.2.7 (88c57354a9f) and v20.2.4. Do not assert that #75958 is closed. |

**Carried by v19.2.7 too** (retitle away from "Tentacle's"; the index rows and the exclusions'
anchors change with the title)

| Entry (line) | Edit |
|---|---|
| Tentacle's DeleteObject removes the head without checking its write tag (:7067) | Releases: v19.2.7, v20.2.4 and main. Drop "the next Squid release carries it too". |
| Tentacle's DeleteObjects asks for MFA for the keys that need none and not for the versions that do (:8557; index :192) | Releases: v19.2.7 and v20.2.1 through v20.2.4. Drop "No v19.2.x tag contains ce98cebb815". rgw-go: main's check on both releases. |
| Tentacle fails an UploadPart that sends If-None-Match: * ... (:7231) | Releases: v20.2.4. v19.2.7 still ignores the conditions. Upstream: ceph/ceph#65932 is first tagged in v19.2.7. |

The nine "still carried" entries of section 2 gain v19.2.7 in their Releases lines.

**New entries.** Each one is unreproduced and derived from the source.
- **radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose x-amz-content-sha256 is unsigned.**
  S3-correctness, fails closed. Fixed in v19.2.7 (1db3ab3b185). Trackers #79803 and #79873. Fixed
  by ceph/ceph#71292 on main and ceph/ceph#71360 on squid; not on the tentacle branch as of
  ab83d026c29. rgw-go: per F4.
- **radosgw's x-amz-expiration reports a NoncurrentVersionExpiration rule.** S3-correctness.
  Affects v19.2.6 and v20.2.4; fixed in v19.2.7 (4dac1850993; tentacle 30daa120099). Tracker
  #77564. rgw-go: no header until phase 2; then the current Expiration rule only, on both releases.
- **Squid's lifecycle transition of an encrypted multipart object drops its part lengths.** Data
  corruption: the object cannot be decrypted. Affects v19.2.6, v20.2.0 and v20.2.1; fixed in
  v19.2.7 (b9b65194f64) and v20.2.2. Tracker #23264. rgw-go: no transition until phase 2, which
  writes the part lengths or skips encrypted objects.
- Conditional: **Squid's DeleteObjects reports another key's result** (C10, tracker #76960). Add it
  only if the re-record shows a flip it explains.

### docs/exclusions.md

| Entry (line) | Edit | Task |
|---|---|---|
| Write conditions are Tentacle's on Squid too (:4225-4256) | v19.2.7 checks PUT conditions as v20.2.4 does. Only the CompleteMultipartUpload bullet stays a Squid difference, now against v19.2.7 (`src/rgw/rgw_rest_s3.cc:4094-4104@v19.2.7#RGWCompleteMultipart_ObjStore_S3::get_params`). The closing paragraph's "merged after v19.2.6" becomes "first tagged in v19.2.7". Keep the heading, which known-differences-squid.txt names, or update both. | F3 |
| Delete conditions are Tentacle's, and a delete is guarded, on both releases (:4257-4296) | Drop the two Squid-zone bullets: v19.2.7 implements the conditions as v20.2.4 does. The guard bullet now covers both zones: v19.2.7 and v20.2.4 remove a replaced head. | F3 |
| Deletes that need MFA are refused (:3610-3627) | The inverted DeleteObjects check is on Squid 19.2.7 zones too. | F3 |
| UploadPart ignores If-Match and If-None-Match (:4673) | The Squid side is v19.2.7, which still passes none. | F3 |
| The metadata sections write only what keeps a user or bucket whole (:1548-1559) | "which neither release's JSON carries" becomes v19.2.7's form. After F9: the document's `obj_lock` when present, the stored one when absent. | F3, then F9 |
| Dynamic resharding worker (:262-318) | Add the missing-shard retry. On Tentacle this is a difference from v20.2.4's 404. | F7 |
| new: SigV4 accepts an unsigned Content-Type and x-amz-content-sha256 on Tentacle too | Only if Q1 is answered yes. | F4 |
| new: The bucket instance document carries `obj_lock` on Tentacle too | Only if Q1 is answered yes. | F8 |

The phase 2 exclusions on x-amz-expiration (:4023-4028 and :4576-4580) are retired by phase 2's
lifecycle unit, not here.

### Other registries

- docs/cgo-limitations.md:553-554: the support policy line (F1).
- docs/porting-process.md:50 and :62 record phase 1's history at v19.2.6 and stay.

## 5. Proposed tasks

Eleven tasks. F1 through F10 need no cluster. **F11 needs a cluster**, comes last, and is the
re-record.

**Order.** F1, F2 and F10 have no dependencies. F3 follows F2. F4, F5 with F6, F7 and F8 follow F3
and touch disjoint code, so they can run in parallel. F9 follows F8. F11 follows F1, F3, F4 and
F10. F5 through F9 do not change any outcome F11 records, so they do not block it.

**Hot files** (PP-5) and how each is kept from serializing the work:
- docs/ceph-upstream-bugs.md (F2, F4, F5, F7, F8, F9) and docs/exclusions.md (F3, F4, F7, F8, F9):
  F2 and F3 make every fact edit and land first. After them, each task edits only its own entry's
  "rgw-go:" paragraph or exclusion bullet, so rebases are trivial.
- test/s3tests/known-differences-squid.txt (F4, F11): F4 adds three lines, and F11 removes them
  with the eight condition lines.
- test/s3tests/known-differences-tentacle.txt: F4 only.

### F1 docs: the spec's Squid floor is v19.2.7

- **Layer:** docs.
- **Files:**
  - docs/superpowers/specs/2026-09-25-rgw-go-design.md §3 (:57-59) and §12 (:501-512);
  - docs/cgo-limitations.md:553-554.
- **Behaviour:** the cluster floor and the librados support policy become Squid 19.2.7. §12 keeps
  the AES256KRB5 parse floor (19.2.6, 20.2.4) as the technical fact behind the policy.
- **Specs:** none. It is docs only, and the s3-tests check skips it.
- **Depends on:** nothing.
- **PR:** its own. **Brief:** about 40 lines.

### F2 docs: the registry at v19.2.7

- **Layer:** docs.
- **Files:** docs/ceph-upstream-bugs.md.
- **Behaviour:**
  - every fact edit in section 4's registry tables: Kind, Evidence, Releases and Upstream lines,
    each citing v19.2.7;
  - the three new entries (the conditional fourth waits for F11);
  - the retitles of the DeleteObject guard and DeleteObjects MFA entries, with their index rows;
  - the nine Releases lines.

  "rgw-go:" lines are left to F4 through F9.
- **Specs:** none. Check the citations with citecheck, and grep for every changed anchor.
- **Depends on:** nothing.
- **PR:** its own. Share it with the rgw-rs session. **Brief:** about 120 lines.

### F3 docs: the exclusions at v19.2.7

- **Layer:** docs.
- **Files:** docs/exclusions.md, plus test/s3tests/known-differences-squid.txt's comments if a
  heading they name changes.
- **Behaviour:** section 4's exclusion edits marked F3. They describe v19.2.7 as the Squid floor,
  with no rgw-go behaviour change. The anchors follow F2's retitles.
- **Specs:** none.
- **Depends on:** F2.
- **PR:** its own. Announce it to the rgw-rs session. **Brief:** about 90 lines.

### F4 auth: accept an unsigned Content-Type and x-amz-content-sha256

- **Layer:** auth.
- **Files:**
  - internal/auth/sigv4.go (`canonicalHeadersV4`, :278-327);
  - the comment on internal/auth/config.go's `Insecure` (:25-29);
  - internal/auth/testdata/v4/;
  - test/s3tests/known-differences-squid.txt and known-differences-tentacle.txt;
  - the registry's and the exclusions' paragraphs for this behaviour.
- **Behaviour:** v19.2.7's `get_v4_canonical_headers` (`src/rgw/rgw_auth_s3.cc:788-842@v19.2.7`):
  - unless insecure, `host` and every `x-amz-*` header except x-amz-content-sha256 must be signed;
  - a present Content-Type need not be;
  - every refusal is still AccessDenied.

  On Tentacle too, as an exclusion against `src/rgw/rgw_auth_s3.cc:774-796@v20.2.4`, unless Q1 says
  otherwise. If Q1 limits it to Squid, auth.Config gains the release, which is the seam.
- **Specs:**
  - the vector unsigned-content-type-rejected becomes -accepted, with its canonical request;
  - new: an unsigned x-amz-content-sha256 (header-signed and presigned) is accepted, and a value
    that differs from what was signed fails the signature, because HashedPayload covers it;
  - new: an unsigned Content-Type beside an unsigned `x-amz-acl` is refused (upstream's
    UnsignedXAmzStillRejectedAlongsideUnsignedContentType invariant);
  - unsigned-x-amz-header-rejected, unsigned-host-rejected and insecure-allows-unsigned-x-amz keep
    their outcomes;
  - presigned with an unsigned Content-Type is accepted.
- **s3-tests:** test_sigv4_contenttype_unsigned, test_sigv4_presigned_contenttype_unsigned and
  test_sigv4_without_signed_content_sha256 now pass on rgw-go.
  - Add them to known-differences-squid.txt. The 19.2.6 baseline says radosgw fails them; F11
    removes the lines once radosgw passes.
  - Add them to known-differences-tentacle.txt, unless Q1 says otherwise.
- **Depends on:** F2, F3.
- **PR:** its own. **Brief:** about 130 lines.

### F5 driver: a re-sent completion answers the stored ETag on both releases

- **Layer:** driver.
- **Files:** internal/driver/mp_complete.go (:286-293), internal/driver/mp_complete_test.go
  (:461-478), and the registry entry's "rgw-go:" line.
- **Behaviour:** `check_previously_completed`'s `etag = oetag` (`src/rgw/rgw_op.cc:6584@v19.2.7`,
  `src/rgw/rgw_op.cc:7472@v20.2.4`). The release split goes. Size, mtime and the unknown-id 500 do
  not change.
- **Specs:** the spec at :461 expects the stored ETag on Squid and Tentacle alike.
- **Depends on:** F2.
- **PR:** shared with F6, which is the same behaviour; two commits. **Brief:** about 40 lines.

### F6 memstore: the same for the in-memory store

- **Layer:** memstore.
- **Files:** internal/memstore/multipart.go (:245-272) and its spec; the s3 table at
  internal/s3/multipart_test.go:889-913, whose Squid entry expects "".
- **Behaviour:** as F5.
- **Specs:** both table entries expect the stored ETag; the memstore spec at :347 likewise.
- **Depends on:** F2.
- **PR:** with F5. **Brief:** about 35 lines.

### F7 driver: a reshard guard that meets a missing shard re-reads the bucket and retries

- **Layer:** driver.
- **Files:**
  - internal/driver/indexop.go (`guardReshard`, :221-252, and `refreshIndexOp`);
  - internal/driver/indexop_test.go;
  - test/integration/write_test.go:403-412;
  - the registry and exclusions paragraphs for this behaviour.
- **Behaviour:** as v19.2.7's guards (`src/rgw/driver/rados/rgw_rados.cc:7225-7230@v19.2.7`,
  `src/rgw/driver/rados/rgw_rados.cc:7929-7933@v19.2.7`):
  - an index call answering ErrNotFound re-reads the bucket instance and retries, within the same
    ten calls;
  - the prepare, the cancel and the completion manager's retry all take this path, as phase 2's
    OLH ops will.

  rgw-go goes further than radosgw on two points:
  - it reads the instance from RADOS, not from a cache entry the reshard's notify may not have
    reached;
  - it retries only when the re-read names another shard object. The same shard returns the
    ENOENT at once, where v19.2.7 repeats it ten times.

  A bucket removed meanwhile is 404 NoSuchKey, as now. On Tentacle this is an exclusion against
  `src/rgw/driver/rados/rgw_rados.cc:7888-7891@v20.2.4` (Q1).
- **Specs** (fakerados; a BeforeWrite hook on the old shard commits a reshard):
  - a prepare whose shard was removed by a committed reshard lands on the new shard, and the write
    succeeds;
  - a completion retry does the same;
  - ENOENT with the same layout returns ErrNotFound after one call;
  - a deleted bucket answers NoSuchKey;
  - busy-resharding retries and ENOENT retries share the bound;
  - the integration spec at write_test.go:403-412 expects no stale-shard write. It runs in the
    nightly integration workflow, and in F11's cluster run.
- **Depends on:** F2, F3.
- **PR:** its own. **Brief:** about 140 lines.

### F8 meta: RGWObjectLock decoded, and the bucket instance JSON carries obj_lock

- **Layer:** meta.
- **Files:** internal/meta/bucket_info.go (:77-80 and :326-385), a new object-lock type in
  internal/meta, and their specs.
- **Behaviour:**
  - decode the stored RGWObjectLock: ENCODE_START(1,1) of `enabled`, `rule_exist` and, when the
    rule exists, ObjectLockRule wrapping DefaultRetention (`mode`, `days`, `years`), each with
    ENCODE_START(1,1) (`src/rgw/rgw_object_lock.h:34-40@v19.2.7`,
    `src/rgw/rgw_object_lock.h:127-135@v19.2.7`);
  - keep the bytes for an exact re-encode;
  - when BUCKET_OBJ_LOCK_ENABLED is set, the dump appends `obj_lock` after `sync_policy`
    (`src/rgw/rgw_common.cc:2538-2544@v19.2.7`), in v19.2.7's form: `enabled` only when true,
    `rule_exist`, then `rule` with `defaultRetention` holding `mode` and either `days` or `years`
    (`src/rgw/rgw_object_lock.cc:26-33@v19.2.7#DefaultRetention::dump`,
    `src/rgw/rgw_object_lock.cc:93-101@v19.2.7#RGWObjectLock::dump`);
  - the JSON decode reads `obj_lock` only under that flag
    (`src/rgw/rgw_common.cc:2588-2590@v19.2.7`).

  On Tentacle too, unless Q1 says otherwise. Otherwise the dump is release-gated, as the zone dump
  is. This can be folded into phase 2's object-lock meta task, if that lands before F11.
- **Specs:**
  - round trips of the default configuration, a GOVERNANCE days rule and a COMPLIANCE years rule;
  - the dump with and without the flag;
  - the JSON decode, with the key missing and with it present;
  - expected JSON hand-built from the v19.2.7 functions above, since the corpus has no such
    instance.
- **Depends on:** F2, F3.
- **PR:** its own. **Brief:** about 120 lines.

### F9 driver: a bucket instance put takes a document's obj_lock

- **Layer:** driver.
- **Files:** internal/driver/metadata.go (`keepInstance` :582-605, `newInstance` :609-630), its
  specs, and the memstore's metadata put if it mirrors the driver's.
- **Behaviour:**
  - with BUCKET_OBJ_LOCK_ENABLED set and an `obj_lock` in the document, the document's
    configuration is stored, for a new instance and an existing one, as v19.2.7's decode does
    (`src/rgw/rgw_common.cc:2588-2590@v19.2.7`);
  - with no `obj_lock`, an existing instance keeps its stored configuration, where v19.2.7 resets
    it (`src/common/ceph_json.h:334-346@v19.2.7`);
  - the refusal to turn object lock off stays.
- **Specs:** a put with a changed rule; a put without `obj_lock` over a ruled instance keeps the
  rule; a new instance with a rule; the refusal unchanged.
- **Depends on:** F8.
- **PR:** its own. It can wait for phase 2's object-lock unit. **Brief:** about 90 lines.

### F10 hack/goldens: v19.2.7's ceph-dencoder by default

- **Layer:** hack.
- **Files:** hack/goldens/gen.sh (:5, :15).
- **Behaviour:** the default image becomes `quay.io/ceph/ceph:v19.2.7`. A regeneration over
  hack/goldens/types.txt shows no change to any golden. Needs podman and the image, not a cluster.
- **Specs:** `git diff --exit-code` over the testdata/goldens trees after `hack/goldens/gen.sh`.
- **Depends on:** nothing.
- **PR:** its own. **Brief:** about 25 lines.

### F11 ops and parity: bump Squid's pin and re-record radosgw's Squid ledgers (needs a cluster)

- **Layer:** ops and parity records.
- **Files:**
  - the pin: hack/rooket/squid/values/rook-ceph-cluster.yaml:5, .goreleaser.yaml:13,
    .github/actions/rooket-cluster/action.yml:48-50, Makefile:219, hack/rooket/README.md:11;
  - the fixture: test/integration/write_test.go:273-291;
  - the records: test/s3tests/baseline/squid.json, test/admin/baseline/squid.json and
    test/s3tests/known-differences-squid.txt.
- **Behaviour:**
  - First check that `quay.io/ceph/ceph:v19.2.7` exists.
  - `make cluster-up RELEASE=squid`, `make populate`, `make gate RELEASE=squid` and `make rgw-go-up`.
  - Record both ledgers in full at ceph_version 19.2.7: `make s3tests-record RELEASE=squid` and
    `make admin-record RELEASE=squid`. A version bump records every test again; neither is a subset
    record.
  - Each changed outcome carries a REASONS line naming the v19.2.7 commit:
    - ce98cebb815 for the eight condition tests;
    - eb63ba5d7de and 1db3ab3b185 for the three SigV4 tests;
    - any other flip is investigated before it is reasoned. An unexplained one stops the task.
  - known-differences-squid.txt loses the eight condition lines and F4's three SigV4 lines. It keeps
    test_multipart_put_object_if_match, the four GetObjectAttributes tests and
    test_expected_bucket_owner. Any of them that agrees now is investigated.
  - If C10 explains a flip, F2's conditional registry entry is added in a follow-up docs PR.
- **Checks:**
  - `make ledger-check`;
  - `make s3tests-parity RELEASE=squid` and `make admin-parity RELEASE=squid`, both clean;
  - `make integration RELEASE=squid`, with write_test.go's collapsed conditions and F7's
    tightened reshard spec;
  - `make release-pins-check`.
- **Depends on:** F1, F3, F4 and F10. Preferably after F5 through F7, so the integration run covers
  them.
- **PR:** one PR, which the stale-baseline rule forces, with two commits:
  1. the pin, CI comment, Makefile, README and integration fixture;
  2. the two ledgers and known-differences-squid.txt.

  Watch the s3-tests check on it.
- **Brief:** about 150 lines.

## 6. Open questions

### Q1 (owner): do the v19.2.7 fixes that v20.2.4 lacks apply on Tentacle too?

v19.2.7 fixes four things that v20.2.4 does not. The tentacle branch carries every fix except the
x-amz-content-sha256 one, but no Tentacle tag does:
- SigV4 accepting an unsigned Content-Type and x-amz-content-sha256 (F4);
- the reshard guard's missing-shard retry (F7);
- `obj_lock` in the bucket instance JSON (F8 and F9);
- in phase 2, x-amz-expiration from the current-version rule only.

On Squid each fix is required, because the floor behaves that way. On Tentacle, the 2026-10-13
rule (the correct S3 behaviour, RADOS state compatible) says to apply them too, each recorded as a
Tentacle exclusion. **Recommended answer: yes, all four.** None changes RADOS state, and each
removes a radosgw defect upstream has already fixed. Applying them on both releases also keeps
release splits out of auth and the reshard guard. The owner's decision is needed for one reason:
F4 relaxes a SigV4 check that radosgw v20.2.4 still enforces, and that check came with the
CVE-2026-54330 hardening. The relaxation is safe because:
- upstream made the same change, keeping every `x-amz-*` check except x-amz-content-sha256's;
- AWS S3 accepts both headers unsigned, as the upstream commit messages record;
- x-amz-content-sha256 stays covered as HashedPayload.

### Coordinator notes (no owner decision needed)

- F9 can wait for phase 2's object-lock unit. F8 can be folded into that unit's meta task, if it
  lands before F11.
- The phase 2 research reports need a v19.2.7 pass in the areas section 1 lists before their tasks
  are planned. Squid's expected values there may now be v20.2.4's.
