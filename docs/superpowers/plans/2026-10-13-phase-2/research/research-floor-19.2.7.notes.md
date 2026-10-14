# Notes: Squid floor v19.2.6 -> v19.2.7

Each verified fact: the claim, its source, the release.

## Baseline

- origin/main is 0239625 (local ref); the phase-1-exec worktree HEAD is af535e4, older, so rgw-go
  is read with `git show origin/main:<path>`.
- `git log --no-merges v19.2.6..v19.2.7 -- src/rgw src/cls` lists 36 commits (rev-list --count); besides the brief's
  list: 05c4a49d28d (assume a nonexistent role crashed), 69c327f161c (abort_early crash on client
  disconnect), 465c3fca115 (neorados CephContext leak), beast/shutdown/notify/finisher internals.
- /tmp/claude-1000/citecheck does not exist; the tool's source is hack/citecheck in the worktree
  agent-a79fe9cce91a94fb3 (commit 95f6d5c, not on origin/main).

## Operational surface (origin/main)

- spec §3 floor text: docs/superpowers/specs/2026-09-25-rgw-go-design.md:57-59 ("Squid 19.2.6 or
  Tentacle 20.2.4", "runs 19.2.6 or later on Squid").
- spec §12 librados text: same file :501-512; the librados floor is the AES256KRB5 key-parse
  floor (19.2.6), a technical fact, and the cluster floor "is the same releases" (:512).
- internal/radosclient/goceph/libversion.go:45-50 libradosFloors {19: 19.2.6}: the first Squid
  librados that parses AES256KRB5 keys. A capability, not the support floor.
- hack/rooket/squid/values/rook-ceph-cluster.yaml:5 `tag: v19.2.6`: the one home of Squid's image.
- hack/rooket/lib.sh:61-69 pinned_version derives the version from the tag; :62 comment example
  "v19.2.6 is 19.2.6". No hardcoded floor. Callers: hack/rooket/up.sh:19-20, populate.sh:21,
  hack/image/build.sh:26-28, hack/image/cgo-build.sh:31-33, hack/image/release-pins-check.sh:37.
- .goreleaser.yaml:13 `SQUID_CEPH_TAG=v19.2.6`; hack/image/release-pins-check.sh:34-42 fails when
  it disagrees with the chart values, so both change in one commit.
- .github/actions/rooket-cluster/action.yml:48-50 comments name 19.2.6 (AES256KRB5 keys; "Squid
  19.2.6 has no noble build"); :60 installs librados from debian-20.2.4 regardless of release.
- Makefile:219 parity-record help text example META "ceph_version=19.2.6".
- hack/goldens/gen.sh:5,15 IMAGE default quay.io/ceph/ceph:v19.2.6 (golden generator).
- hack/rooket/README.md:11 "quay.io/ceph/ceph:v19.2.6 under Rook v1.20.7"; :87 "librados 19.2.6 or
  later".


## C1 f6877f01fe4 (GC enqueue of the overwritten object) and C2 ce98cebb815 (conditional delete)

- v19.2.6 write path: `_do_write_meta` reads state into locals, then
  `prepare_atomic_modification` re-reads it into the Object members and checks If-Match and
  If-None-Match inside `need_guard`:
  [rgw_rados.cc:3145-3149](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3145-L3149),
  [rgw_rados.cc:3171](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L3171),
  [rgw_rados.cc:6489-6544](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L6489-L6544).
- v19.2.7 write path: members set from the locals, then `get_current_version_state` for a
  versioned instance, `check_preconditions`, and the guard only when `set_attr_id_tag` (no
  versioned instance, not If-None-Match *):
  [rgw_rados.cc:3185-3205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3185-L3205). Identical at
  [rgw_rados.cc:3282-3302](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3282-L3302).
- `check_preconditions` and `get_current_version_state` are byte-identical between
  [rgw_rados.cc:6624-6780](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6624-L6780) and
  [rgw_rados.cc:7238-7394](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7238-L7394) (diff of the two ranges is empty).
  If-Match * on a missing key -> -ENOENT, an ETag on a missing key -> -ENOENT, quoted conditions
  unquoted, prefix compare kept: [rgw_rados.cc:6673-6712](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6673-L6712).
- f6877f01fe4 (applied after ce98cebb815) replaced ce98cebb815's second `get_state` in
  `_do_write_meta` with member assignment and moved a `get_state` into
  `complete_atomic_modification`: [rgw_rados.cc:5455-5459](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5455-L5459); same at
  [rgw_rados.cc:6102-6106](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6102-L6106); absent at
  [rgw_rados.cc:5382-5385](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5382-L5385). It repairs ce98cebb815's own regression:
  the delete path no longer calls `prepare_atomic_modification`, which was what loaded the members
  `complete_atomic_modification` reads. Net between the tags: the overwritten or deleted head's
  tails are still queued from the state read before the op. No RADOS difference from v19.2.6.
- v19.2.6 delete: `prepare_atomic_modification(removal_op)` appends the idtag cmpxattr ahead of
  the removal: [rgw_rados.cc:5914](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5914).
- v19.2.7 delete: no `prepare_atomic_modification` call (only callers are
  [rgw_rados.cc:3203](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3203) and the definition); `check_preconditions` plus
  `obj_check_mtime` EQ: [rgw_rados.cc:6049-6057](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6049-L6057); versioned
  conditions against the current version or the instance's dirent:
  [rgw_rados.cc:5882-5897](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5882-L5897),
  [rgw_rados.cc:5913-5942](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5913-L5942). So 19.2.7 carries the registry's
  "Tentacle's DeleteObject removes the head without checking its write tag".
- v19.2.7 S3 DeleteObject reads If-Match, x-amz-if-match-last-modified-time, x-amz-if-match-size
  (ce98cebb815's `RGWDeleteObj_ObjStore_S3::get_params` hunk); DeleteObjects parses ETag,
  LastModifiedTime, Size per Object (`src/rgw/rgw_multi_del.cc` hunk).
- v19.2.7 DeleteObjects MFA check inverted: [rgw_op.cc:7067-7080](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7067-L7080) tests
  `instance.empty()`; v19.2.6 tests `!i.instance.empty()`:
  [rgw_op.cc:7055-7068](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L7055-L7068); v20.2.4 inverted: [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983).
  The rest of `RGWDeleteMultiObj::execute` is unchanged between the tags (range diff).
- rgw-go: write conditions are v20.2.4's on both releases: internal/driver/headwrite.go:110-145
  (checkPreconditions). guardHead skips the guard for a versioned instance on Tentacle only:
  internal/driver/headwrite.go:342-344, so on Squid it still guards as v19.2.6 did.
- rgw-go: delete keeps the idtag guard on both releases (docs/exclusions.md:4257-4296); MFA check
  as v19.2.6 and main on both: internal/op/deleteobjects.go:181-187.
- test/integration/write_test.go:273-291 asserts v19.2.6's PUT condition outcomes (412, 412, 412)
  against the live Squid radosgw; at 19.2.7 radosgw answers v20.2.4's (404, 200, 200).

## C10 41984610868 (local error code in handle_individual_object)

- v19.2.6 writes the shared member `op_ret` from concurrent coroutines and reports it per key:
  [rgw_op.cc:6879](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6879), [rgw_op.cc:6894](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_op.cc#L6894); v19.2.7 uses a local `r`:
  [rgw_op.cc:6888](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6888), [rgw_op.cc:6906](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6906); v20.2.4 local:
  [rgw_op.cc:7816](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7816), [rgw_op.cc:7834](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7834). Tracker #76960.
- rgw-go computes each key's result locally: internal/op/deleteobjects.go:229-248. No registry
  entry names it (grep 76960 and op_ret).

## C14 88c57354a9f (rgw_gc_max_objs startup-only)

- `startup` flag added: [rgw.yaml.in:1713-1714](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L1713-L1714); present at
  [rgw.yaml.in:1795-1796](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1795-L1796). IOManager sized from the stored max_objs.
  Fixes tracker #75958 (crash raising it at runtime) on Squid.
- rgw-go reads rgw_gc_max_objs once at Open: internal/driver/writer.go:97-103; no runtime reload.
- Registry names #75958 as related in "radosgw faults on a zero rgw_gc_max_objs ..."
  (docs/ceph-upstream-bugs.md:615-616).

## C2 continued: conditions that 19.2.7 still lacks, and Squid-only rgw-go splits

- 19.2.7 CompleteMultipartUpload reads no If-Match / If-None-Match:
  [rgw_rest_s3.cc:4094-4104](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4094-L4104).
  So test_multipart_put_object_if_match stays a Squid known difference.
- `RGWDeleteObj_ObjStore_S3::get_params` is identical between
  [rgw_rest_s3.cc:3435-3500](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3500) and [rgw_rest_s3.cc:3685-3750](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3750);
  `src/rgw/rgw_multi_del.cc` and its header are identical at v19.2.7 and v20.2.4 (git diff empty);
  `Delete::delete_obj` differs only in datalog error handling and the op move
  ([rgw_rados.cc:5833-6127](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5833-L6127) vs
  [rgw_rados.cc:6445-6745](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L6445-L6745)).
- 19.2.7 `MultipartObjectProcessor::complete` is unchanged (rgw_putobj_processor.cc is not in the
  v19.2.6..v19.2.7 diffstat), so UploadPart still passes no conditions on Squid.
- rgw-go's DeleteObjects refusal split (MalformedXML message on Tentacle,
  internal/s3/deleteobjects.go:142-148; empty-list refusal, internal/op/deleteobjects.go:171-180)
  is Tentacle's own, unchanged by 19.2.7.

## C8 4b567265365 (ETag of a re-sent CompleteMultipartUpload)

- Adds `etag = oetag;` to `check_previously_completed` (tracker #75999). Registry entry "Squid
  answers a re-sent CompleteMultipartUpload of a finished upload with an empty ETag"
  (docs/ceph-upstream-bugs.md:9103-9142) says rgw-go reproduces it per release.
- rgw-go driver: internal/driver/mp_complete.go:286-293 sets the ETag only on Tentacle;
  memstore: internal/memstore/multipart.go:267-270 clears it below Tentacle. Specs:
  internal/driver/mp_complete_test.go:461, internal/memstore/multipart_test.go:347,
  internal/s3/multipart_test.go:889.
- No s3-test in the ledger covers a re-sent completion (ledger scan for multipart|complete).

## C3, C4 1db3ab3b185 and eb63ba5d7de (SigV4 signed headers)

- v19.2.7 no longer requires a present Content-Type to be signed and skips
  x-amz-content-sha256 in the unsigned x-amz-* scan: [rgw_auth_s3.cc:788-842](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L788-L842).
  v19.2.6 required Content-Type: [rgw_auth_s3.cc:798-802](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_auth_s3.cc#L798-L802). v20.2.4 still requires
  both: [rgw_auth_s3.cc:774-796](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L774-L796). Squid 19.2.7 is ahead of Tentacle 20.2.4 here.
- x-amz-content-sha256 enters the canonical request as HashedPayload whether signed or not
  ([rgw_auth_s3.h:638-660](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.h#L638-L660), used at
  [rgw_rest_s3.cc:5858](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5858)), so leaving it unsigned cannot change it unseen.
- rgw-go requires both on every release: internal/auth/sigv4.go:305-318 (canonicalHeadersV4);
  auth.Config has no release (internal/auth/config.go:13-38). Vector
  internal/auth/testdata/v4/unsigned-content-type-rejected.json pins the refusal.
- Registry: "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned"
  (docs/ceph-upstream-bugs.md:927-970, index row :41). No entry for x-amz-content-sha256 (#79803,
  backport #79873, squid PR #71360).
- Ledger: test_sigv4_contenttype_unsigned, test_sigv4_presigned_contenttype_unsigned and
  test_sigv4_without_signed_content_sha256 are failed on both releases at 19.2.6/20.2.4; expected
  to pass on 19.2.7.

## Parity mechanics (origin/main)

- hack/parity diff judges a last entry valid only when its ceph_version is the candidate's
  (hack/parity/main.go:194-205); CI passes no --allow-meta-drift (Makefile:243, Makefile:257,
  .github/workflows/integration.yml:94). So a Squid image at 19.2.7 makes all 416 s3-tests and 80
  admin Squid entries "baseline stale, re-record": the pin bump and the re-record share a PR.
- A known-difference line whose test agrees fails as stale (hack/parity/main.go:207-218).
  test/s3tests/known-differences-squid.txt lists 3 PUT-condition, 6 delete-condition, 4
  GetObjectAttributes and 1 expected-bucket-owner tests; known-differences-tentacle.txt is empty.
- Squid vs Tentacle radosgw ledgers differ in 15 s3-tests (14 above plus
  test_bucket_policy_allow_notprincipal) and 2 admin tests (TestAccount get/delete, skipped on
  Squid).
- s3-tests pin f6f2529dd9 is on ceph main's src/test/rgw/s3-tests (hack/s3tests/run.sh:26-31), not
  the squid branch, so 19.2.7's in-tree s3-tests changes do not move it.

## C5, C6 148e1d182a0 and 7b8039bb2a8 (outgoing AWSSignerV4)

- radosgw's own signer now always sends x-amz-content-sha256 (UNSIGNED-PAYLOAD, or a forwarded
  request's value). v20.2.4 has both: [rgw_rest_s3.cc:6212-6227](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L6212-L6227). Squid prepare
  is at [rgw_rest_s3.cc:5664](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5664).
- rgw-go signs no outgoing request: it neither forwards nor receives forwarded requests
  (docs/exclusions.md:3165-3166, Multisite); cloud transition is outside phase 2 (spec §9 lists
  in-cluster storage-class transition only, docs/superpowers/specs/2026-09-25-rgw-go-design.md:363-371).
  No change.

## C7 596eaf34725 (guard_reshard retries ENOENT)

- v19.2.7 `UpdateIndex::guard_reshard` retries an ENOENT after `invalidate_bs()`:
  [rgw_rados.cc:7225-7230](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7225-L7230); `RGWRados::guard_reshard` likewise:
  [rgw_rados.cc:7929-7933](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7929-L7933). Both within NUM_RESHARD_RETRIES 10
  ([rgw_rados.cc:7216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7216)).
- The retry re-reads the bucket: `init_bs` passes `&target->bucket_info`
  ([rgw_rados.h:947-955](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L947-L955)), and that `BucketShard::init` overload
  always calls `get_bucket_instance_info` into it
  ([rgw_rados.cc:2804-2830](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2804-L2830)).
- Callers: prepare [rgw_rados.cc:7289-7291](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7289-L7291), cancel
  [rgw_rados.cc:7395-7397](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7395-L7397), the completion retry thread
  [rgw_rados.cc:905](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L905), and the OLH ops (link_olh, unlink_instance,
  trim_olh_log, clear_olh) [rgw_rados.cc:8153](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8153),
  [rgw_rados.cc:8207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8207), [rgw_rados.cc:8349](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8349),
  [rgw_rados.cc:8380](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8380).
- v20.2.4 has no ENOENT retry: [rgw_rados.cc:7888-7891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7888-L7891). Upstream main
  commit e34067f8cea is first tagged in v21.0.0; squid PR ceph/ceph#70498 (merge fdbef6c8b7f).
- This fixes, on Squid, the registry's "radosgw fails a write whose bucket was resharded since the
  request read it" (docs/ceph-upstream-bugs.md:11770-11813, index row :257), which says rgw-go
  reproduces it: internal/driver/indexop.go:227-252 retries only ErrBusyResharding.
- test/integration/write_test.go:403-412 tolerates one stale-shard 404 and re-sends.

## C9 8001a4d4fd8 (copy_obj_data accounted size)

- v19.2.7 passes the source's accounted size and uses it when the destination is not compressed:
  [rgw_rados.cc:5145-5146](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5145-L5146); v19.2.6 and v20.2.4 use `ofs`:
  [rgw_rados.cc:5108](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5108), [rgw_rados.cc:5378](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5378).
- On Squid `copy_obj` refuses an encrypted source and always carries the source's compression
  attr into the copy's attrs: [rgw_rados.cc:4787-4795](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4787-L4795),
  [rgw_rados.cc:4823-4825](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L4823-L4825). So a compressed source reads
  `compressed` true, and an uncompressed one has `ofs` equal to its size: no observable change on
  Squid.
- rgw-go takes the accounted size from the compression info it keeps (internal/driver/copy.go:424-450)
  and refuses encrypted sources (internal/driver/copy.go:127-135). No change.

## C13 b9b65194f64, 42fe9817449 (transition of encrypted multipart; cloud-tier resume)

- v19.2.7 `transition_obj` records RGW_ATTR_CRYPT_PARTS for an encrypted source before it rewrites
  the manifest: [rgw_rados.cc:5155-5182](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5155-L5182),
  called at [rgw_rados.cc:5211](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5211); same at
  [rgw_rados.cc:5388](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5388) and [rgw_rados.cc:5444](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L5444);
  absent at v19.2.6 (`transition_obj` at [rgw_rados.cc:5117](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_rados.cc#L5117)).
  Phase 2 storage-class transition input: a transition of an encrypted multipart object must write
  the part lengths (or refuse), on both releases. rgw-go has no transition yet.
- 42fe9817449 touches only rgw_lc_tier.cc (cloud tier): out of scope.

## C14b 88c57354a9f's companion: rgw.yaml.in also documents rgw_lc_max_objs as not to be changed
  after deployment ([rgw.yaml.in:432-433](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L432-L433)), text only.

## C11 4dac1850993 (x-amz-expiration for the current version only)

- v19.2.7 returns no header for a versionId request and computes it from the Expiration rule alone:
  [rgw_lc.cc:2750](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2750), the rule at
  [rgw_lc.cc:2850](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2850); the same commit's REST helper passes the client's key and
  versionId, not the OLH-resolved key (its `get_s3_expiration_header` hunk). v19.2.6 and v20.2.4 pick
  the noncurrent rule for a key with an instance:
  [rgw_lc.cc:2849](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_lc.cc#L2849), [rgw_lc.cc:2916](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2916). Tracker #77564.
- rgw-go sends no x-amz-expiration until phase 2 (docs/exclusions.md:4023-4028, :4576-4580).
  Phase 2 lifecycle input; no registry entry names #77564.

## C12 3f9eb56d5ec (bucket instance JSON carries obj_lock)

- v19.2.7 `RGWBucketInfo::dump` writes `obj_lock` when object lock is enabled and `decode_json`
  reads it: [rgw_common.cc:2542-2544](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2542-L2544), [rgw_common.cc:2588-2590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590).
  v20.2.4 has neither (only the binary encode/decode at [rgw_common.cc:2363-2364](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2363-L2364)).
- v19.2.7 changes the RGWObjectLock JSON form: `enabled` only when true, `rule` holding
  `defaultRetention` (was `default_retention`), via encode_json:
  [rgw_object_lock.cc:26-33](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L26-L33),
  [rgw_object_lock.cc:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L56-L58),
  [rgw_object_lock.cc:93-101](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L93-L101); old form at
  [rgw_object_lock.cc:77](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L77). Decoders:
  [rgw_object_lock.cc:8](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L8),
  [rgw_object_lock.cc:65](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L65).
- rgw-go's dump has no obj_lock and says "Squid and Tentacle write it; main adds obj_lock":
  internal/meta/bucket_info.go:326-385. ObjLock is held opaque (internal/meta/bucket_info.go:77-80).
  Its put keeps a stored instance's obj_lock and gives a new one the default:
  internal/driver/metadata.go:592-598, internal/driver/metadata.go:628.
- Registry "radosgw's bucket instance metadata put drops the bucket's object lock rule"
  (docs/ceph-upstream-bugs.md:11384-11441) already says fixed in v19.2.7. Exclusion text "which
  neither release's JSON carries" (docs/exclusions.md:1550-1554) is false at 19.2.7.
- No corpus RGWBucketInfo golden has BUCKET_OBJ_LOCK_ENABLED (flags seen: 0, 1, 2), so regenerating
  goldens at 19.2.7 cannot change RGWBucketInfo's JSON; no RGWObjectLock type in
  hack/goldens/types.txt. No encode changes between the tags.
- Population creates no object-lock bucket (grep of hack/rooket/populate.sh and test/gate).

## Internals with no client- or RADOS-visible change

- Notify service: v19.2.7 starts RGWSI_Notify only with the cache (`have_cache`) instead of
  `!raw`: [rgw_service.cc:73-75](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_service.cc#L73-L75); same at
  [rgw_service.cc:66-68](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_service.cc#L66-L68). Control object names unchanged.
- 69c327f161c catches a send failure in abort_early (crash fix); 05c4a49d28d stops an admin
  AssumeRole of a missing role crashing (STS, phase 3); beast, shutdown, finisher, http client,
  datalog and neorados commits are process-internal.

## Squid-branch registry entries that v19.2.7 still carries (files unchanged between the tags)

- "Squid does not guard listing-time index suggestions against resharding" (:280, Releases :292);
  "radosgw's notification queue listing never pages past 1024 queues" (:1648, :1659);
  "Squid's realm reload hangs ..." (:1671, :1712-1713); "Squid's negated condition operators ..."
  (:4572, :4609-4611); "Squid's is_public counts ..." (:5951, :5975-5977); "radosgw's gc stalls a
  converted shard ..." (:7523, :7557-7560); "Squid labels a copy ... with its source's class"
  (:7953, :7997); "radosgw's CompleteMultipartUpload does not keep its lock ..." (:8984, :9003);
  "radosgw stores request credentials as object attrs" (:10103, :10145-10146). All lines are
  docs/ceph-upstream-bugs.md at origin/main. v19.2.7 is a742f50616e plus the version commit
  9ec952b9f96 (tagged 2026-10-08), and v19.2.6 is its ancestor (git log v19.2.7..v19.2.6 empty).

## Unverified here (no network: download.ceph.com and quay.io are not on the sandbox allowlist)

- That quay.io/ceph/ceph:v19.2.7 is published, and whether download.ceph.com has a noble build of
  19.2.7 (the CI action installs debian-20.2.4's librados regardless).

## Upstream provenance (ceph clone refs origin/main, origin/tentacle at ab83d026c29, 2026-10-12)

- x-amz-content-sha256 unsigned: main 1890af92ec3 via ceph/ceph#71292 (merge e1ecde02004,
  2026-08-25; branch wip-79803); squid 1db3ab3b185 via ceph/ceph#71360 (merge d23da67eaa3); not on
  origin/tentacle after v20.2.4 (git log -S of its comment finds nothing).
- reshard ENOENT retry: main e34067f8cea via ceph/ceph#66553 (merge bc119378344, 2026-02-12), first
  tagged v21.0.0; squid 596eaf34725 via ceph/ceph#70498 (merge fdbef6c8b7f); tentacle 183829b915c,
  after v20.2.4, in no tentacle tag.
- x-amz-expiration: tentacle 30daa120099, after v20.2.4; squid 4dac1850993 (v19.2.7).
- Transition of encrypted multipart (fixup_manifest_to_parts_len): absent at v19.2.6, v20.2.0 and
  v20.2.1; present from v20.2.2 and at v19.2.7.
- `JSONDecoder::decode_json` resets a missing non-mandatory member to `T()`:
  [ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346). So v19.2.7's bucket instance put of a document without
  `obj_lock` still replaces the rule with RGWObjectLock(), enabled and ruleless.
