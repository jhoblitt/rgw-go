# Phase 2 unit F: the Squid floor at v19.2.7

Threat model: F4 sits at the spec's authentication boundary and defends against a client
adversary: which headers a SigV4 signature must cover, the CVE-2026-54330 hardening. It gets
adversarial review (PP-14). Every other task defends against honest mistakes: a bucket instance
document that loses an object lock rule (F8, F9), an index write that meets a reshard (F7), a
stale ledger, pin or registry fact (F1 to F3, F10, F11).

Source: `research/research-floor-19.2.7.md`, with
task ids F1 to F11 kept. rgw-go paths are at origin/main 0239625. Line numbers in
`docs/ceph-upstream-bugs.md` and `docs/exclusions.md` are at that commit too. Where this file
departs from the report, the drafting notes says why.

## Scope

The owner approved on 2026-10-13 moving the Squid floor of spec §3 ("Ceph floor Squid 19.2.6 or
Tentacle 20.2.4") from v19.2.6 to v19.2.7 (`CMakeLists.txt:3-4@v19.2.7`, the version commit
9ec952b9f96, tagged 2026-10-08). Tentacle stays at v20.2.4 (`CMakeLists.txt:3-4@v20.2.4`).
v19.2.6 is an ancestor of v19.2.7, so the move adds only what the squid branch gained.
Compatibility is spec §8's rule: RADOS state in both directions, not radosgw's defects.

**F tasks are phase 1 maintenance.** All of them land before any phase 2 task, S1 to S3
included, and before spec §9's deferred benchmark run, so the benchmarks measure rgw-go and
radosgw at the v19.2.7 floor (index, "Before phase 2's tasks start").

| Item | Tasks |
|---|---|
| The spec's cluster floor and librados support policy at 19.2.7 | F1 |
| The registry's facts at v19.2.7, with three new entries | F2 |
| The exclusions' facts at v19.2.7 | F3 |
| SigV4: an unsigned Content-Type and x-amz-content-sha256 accepted, once shown safe | F4 |
| A re-sent CompleteMultipartUpload answers the stored ETag, driver and memstore | F5, F6 |
| A reshard guard that meets a missing shard re-reads the bucket and retries | F7 |
| `obj_lock` in the bucket instance JSON: the dump and decode, then the put | F8, F9 |
| The golden generator's image | F10 |
| Squid's cluster pin, and radosgw's Squid ledgers re-recorded at 19.2.7 | F11 |

Not in this unit:
- x-amz-expiration in v19.2.7's form is C2's and C8's (C-D7). The part lengths of an encrypted
  multipart object on transition are C11's. Both were amended in their units with this retag.
- The commits that change nothing rgw-go does: the GC enqueue repair f6877f01fe4, radosgw's
  outgoing signer (148e1d182a0, 7b8039bb2a8), copy_obj_data's accounted size 8001a4d4fd8,
  DeleteObjects' per-key error code 41984610868, the `rgw_gc_max_objs` startup flag 88c57354a9f,
  and the internals (report §2).
- Whether the registry's Squid-rule "rgw-go:" lines should still reproduce Squid under the
  2026-10-13 rule is the separate compatibility sweep.

## Decisions

The unit carries the owner's decision P5. The unit's own decisions are FD-1 to FD-9.

| Id | Decision | Reason | Tasks |
|---|---|---|---|
| P5 (the owner, 2026-10-13) | Every v19.2.7 fix that v20.2.4 lacks applies on Tentacle too, each recorded as a Tentacle exclusion: the reshard guard's missing-shard retry (F7), `obj_lock` in the instance JSON (F8, F9), x-amz-expiration's current-version rule (C2, phase 2), and the SigV4 relaxation (F4). The SigV4 relaxation carries a precondition: F4 first shows that it does not reopen CVE-2026-54330 and quotes AWS's own rule. If either check fails, F4 keeps Tentacle's stricter check and records why, and the owner sees that outcome before the merge. | Each fix changes no RADOS state and removes a defect upstream has fixed (the report's Q1). The SigV4 relaxation loosens a check that came with the CVE-2026-54330 hardening and that a released Tentacle still enforces, so it is proved, not assumed. | F4, F7, F8, F9 |
| FD-1 | The F tasks land before phase 2 and before the deferred benchmark run (Scope). F8 and F9 land here too, not folded into the L unit as the report allowed. | The benchmarks then measure the v19.2.7 floor. A get from rgw-go and a put into a v19.2.7 radosgw resets a rule as soon as the floor moves, so the JSON cannot wait for phase 2. | all |
| FD-2 | F2 and F3 make every fact edit in the registry and the exclusions and land first. After them each behaviour task edits only its own entry's "rgw-go:" line and exclusion bullet. | Both files are hot (PP-5) and shared with rgw-rs; facts true of v19.2.7 whatever rgw-go does belong in one review each. | F2 to F9 |
| FD-3 | The pin bump and the Squid re-record are one PR (F11). | hack/parity counts a baseline entry only when its `ceph_version` is the candidate's (hack/parity/main.go:194-205), and CI passes no `--allow-meta-drift` (Makefile:243, :257). The moment the cluster runs 19.2.7 every Squid entry reads stale. | F11 |
| FD-4 | F4 adds no release to `auth.Config`: under P5 the signed-header rule is the same on both releases. Only if F4's precondition ends with the releases split does `auth.Config` gain `Release denc.Release`, as `authz.Config` carries it (internal/authz/config.go:20), with `DefaultConfig` and `ConfigFrom` taking the release so no caller gets the relaxed rule by default. | `auth.Config` has no release today (internal/auth/config.go:13-38); a seam nobody needs is not added. | F4 |
| FD-5 | F4's three s3-tests go into both known-differences files. On Tentacle they stay, under F4's exclusion. On Squid they are transitional: their comment names the two registry entries, not an exclusion, since rgw-go then matches the floor and the difference is with a v19.2.6 ledger that F11 retires and removes them from. | A known difference that agrees fails as stale (hack/parity/main.go:207-218), so each line lands with the change that makes it differ. | F4, F11 |
| FD-6 | The reshard retry goes further than radosgw's: it re-reads the instance from RADOS, and retries only when the re-read names another shard object. The same shard returns at once. | v19.2.7 retries blindly up to ten times, and its re-read can meet a cache the reshard's notify has not reached. | F7 |
| FD-7 | F8 builds the typed RGWObjectLock in `internal/meta/objectlock.go` under the names L1 gives it (L-D5), so L1 consumes it instead of creating it. `BucketInfo.ObjLock` stays a RawStruct. | One owner for one type; L-D5 keeps the lock out of the hot `bucket_info.go`, which F8 edits only for the dump. | F8, L1 |
| FD-8 | A bucket instance put without `obj_lock` keeps the stored configuration, where v19.2.7 resets it to RGWObjectLock(). | The compatibility rule: prevent corruption of shared data even where radosgw allows it. The reset drops a WORM bucket's default retention. Exclusion. | F9 |
| FD-9 | F5 adds an s3-test for a re-sent completion's ETag to jhoblitt/ceph's `rgw-go-s3tests` branch; the pin bump is its own change. F7, F8 and F9 add none. | The index's s3-tests rule. No s3-test can make a reshard land between a request's read and its prepare, and the metadata API is not S3. | F5 |

## Seams

**Consumed**
- hack/parity's ledgers: `record`, `diff`, `check`, the stale-baseline rule and the stale
  known-difference rule (FD-3, FD-5).
- Phase 1's `indexOp.guardReshard`, `blockWhileResharding` and `refreshIndexOp`
  (internal/driver/indexop.go:221-321) (F7).
- Phase 1's `meta.BucketInfo`, its `MarshalJSON` (internal/meta/bucket_info.go:326-385) and
  `UnmarshalJSON` (internal/meta/bucket_json.go:92-160) (F8); `keepInstance` and `newInstance`
  (internal/driver/metadata.go:580-630) (F9).
- Phase 1's `canonicalHeadersV4` (internal/auth/sigv4.go:278-327) and `auth.Config` (F4).

**Produced**

| Seam | Task | Consumers |
|---|---|---|
| Only on FD-4's fallback: `auth.Config.Release denc.Release`; `func DefaultConfig(r denc.Release) Config`; `func ConfigFrom(o *cephconf.Options, dnsNames []string, r denc.Release) (Config, error)` | F4 | `internal/cli/serve.go` (from `store.Release()`), every auth spec |
| `indexOp.guardReshard` retrying a missing shard, for every index call | F7 | V5a and V5b (the OLH ops) |
| `type DefaultRetention struct{ Mode string; Days, Years int32 }`, `type ObjectLockRule struct{ DefaultRetention DefaultRetention }`, `type ObjectLock struct{ Enabled bool; Rule *ObjectLockRule }` in `internal/meta/objectlock.go` | F8 | F9, L1 (which then adds only its XML and evaluation) |
| `func (b *BucketInfo) ObjectLock() (ObjectLock, error)`, `func (b *BucketInfo) SetObjectLock(ObjectLock)` | F8 | F9, L1, L2 |
| `BucketInfo.UnmarshalJSON` leaves `ObjLock` nil exactly when the document carries no `obj_lock` under the flag | F8 | F9 |
| Squid's s3-tests and admin ledgers at `ceph_version` 19.2.7 | F11 | every phase 2 recording task |

## Hot files

| File | Tasks | Rule |
|---|---|---|
| `docs/ceph-upstream-bugs.md` | F2, F4, F5, F6, F7, F8, F9 | F2 first, every fact; then each task only its entry's "rgw-go:" paragraph (FD-2). Shared with rgw-rs. |
| `docs/exclusions.md` | F3, F4, F7, F8, F9 | F3 first, every fact; then each task only its own bullet. Announced to the rgw-rs session. |
| `test/s3tests/known-differences-squid.txt` | F3 (comments, if a heading changes), F4, F11 | F4 adds three lines; F11 removes them with the eight condition lines. |
| `test/s3tests/known-differences-tentacle.txt` | F4 | single owner here; F4's three lines stay. |
| `test/integration/write_test.go` | F7 (:403-412), F11 (:273-291) | separate hunks; F7 first. |
| `internal/meta/objectlock.go` | F8, then L1 | F8 adds the lock types; L1 adds after it. |
| `internal/meta/bucket_info.go`, `bucket_json.go` | F8 | single owner. |
| `internal/driver/metadata.go` | F9 | single owner here. |

## Tasks

### Task F1: docs, the spec's Squid floor is v19.2.7

- **Layer:** docs. **Cluster:** no.
- **Files:** modify docs/superpowers/specs/2026-09-25-rgw-go-design.md §3 (:57-59) and §12
  (:501-512); docs/cgo-limitations.md:553-554.
- **Behaviour:**
  1. §3's floor reads "Squid 19.2.7 or Tentacle 20.2.4", and "runs 19.2.7 or later on Squid"
     (`CMakeLists.txt:3-4@v19.2.7`).
  2. §12's support policy reads "librados 19.2.7 or later on Squid". It keeps the technical
     fact that does not move: AES256KRB5 keys parse from librados 19.2.6 and 20.2.4. "The cluster
     floor in section 3 is the same releases" stays true.
  3. docs/cgo-limitations.md's support-policy line changes the same way; its benchmark table
     (:44-46) is a record and stays.
  4. Unchanged, by decision: `libradosFloors` (internal/radosclient/goceph/libversion.go:45-50)
     and its message (:87-88) are the AES256KRB5 parse floor, a capability; rgw-go enforces no
     support floor at run time. docs/porting-process.md:50 and :62 record phase 1's history.
- **Specs:** none; the s3-tests check skips docs.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** its own. **Brief:** about 40 lines.

### Task F2: docs, the registry at v19.2.7

- **Layer:** docs. **Cluster:** no.
- **Files:** modify docs/ceph-upstream-bugs.md.
- **Behaviour** (report §4; every fact cites v19.2.7 in the checked form):
  1. **Fixed in v19.2.7**, the Kind, Evidence, Releases and Upstream lines:
     - "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose Content-Type is unsigned" (:927,
       index :41): fixed by eb63ba5d7de (squid ceph/ceph#71296, merge 18d35e32770); Tentacle
       unfixed through v20.2.4, the tentacle branch carries it. The title stays true.
     - "Squid's PUT refuses a quoted If-Match ..." (:6321): the quoted rejection and the 412 on a
       missing key are fixed by ce98cebb815; the prefix comparison is unfixed at both floors;
       ceph/ceph#65932 is first tagged in v19.2.7.
     - "Squid's write conditions fail an If-None-Match ETag on a missing key ..." (:6390):
       Releases v19.2.6 only.
     - "Squid answers a re-sent CompleteMultipartUpload ... with an empty ETag" (:9103): fixed by
       4b567265365.
     - "radosgw fails a write whose bucket was resharded since the request read it" (:11770,
       index :257): fixed by 596eaf34725 (squid ceph/ceph#70498; main e34067f8cea through
       ceph/ceph#66553, first tagged in v21.0.0; tentacle 183829b915c, in no tag); Releases
       v19.2.6 and v20.2.4; Upstream no longer "pending".
     - "radosgw's bucket instance metadata put drops the bucket's object lock rule" (:11384): it
       already says fixed in v19.2.7. Add the residual: v19.2.7 still resets the rule for a
       document without `obj_lock`, since a missing member decodes as `T()`
       (`src/common/ceph_json.h:334-346@v19.2.7`).
     - "radosgw faults on a zero rgw_gc_max_objs ..." (:550): the runtime-change crash is kept
       from happening by the `startup` flag at both floors
       (`src/common/options/rgw.yaml.in:1713-1714@v19.2.7`,
       `src/common/options/rgw.yaml.in:1795-1796@v20.2.4`). #75958 is not asserted closed.
  2. **Carried by v19.2.7 too**, retitled away from "Tentacle's", with their index rows and the
     anchors `docs/exclusions.md` and the known-differences comments use:
     - "Tentacle's DeleteObject removes the head without checking its write tag" (:7067):
       Releases v19.2.7, v20.2.4 and main
       (`src/rgw/driver/rados/rgw_rados.cc:6049-6057@v19.2.7`).
     - "Tentacle's DeleteObjects asks for MFA for the keys that need none ..." (:8557, index
       :192): Releases v19.2.7 and v20.2.1 to v20.2.4 (`src/rgw/rgw_op.cc:7067-7081@v19.2.7`,
       `src/rgw/rgw_op.cc:7969-7983@v20.2.4`); drop "No v19.2.x tag contains ce98cebb815".
     - "Tentacle fails an UploadPart that sends If-None-Match: * ..." (:7231): Releases v20.2.4;
       v19.2.7 still ignores the conditions.
  3. **The nine entries v19.2.7 still carries** gain v19.2.7 in their Releases lines (report §2,
     "Registry entries v19.2.7 still carries"; their code is unchanged between the tags).
  4. **New entries**, each unreproduced and derived from the source:
     - "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose x-amz-content-sha256 is
       unsigned": S3-correctness, fails closed; fixed in v19.2.7 by 1db3ab3b185
       (`src/rgw/rgw_auth_s3.cc:788-842@v19.2.7`, `src/rgw/rgw_auth_s3.cc:765-797@v20.2.4`);
       trackers #79803 and #79873; ceph/ceph#71292 (main) and ceph/ceph#71360 (squid);
       the tentacle branch carries it too, through ceph/ceph#71364 (merge 7e2e2072822, a1dd71be880), in no Tentacle tag yet.
     - "radosgw's x-amz-expiration reports a NoncurrentVersionExpiration rule": S3-correctness;
       v19.2.6 and v20.2.4 (`src/rgw/rgw_lc.cc:2913-2928@v20.2.4`); fixed in v19.2.7 by
       4dac1850993 (`src/rgw/rgw_lc.cc:2750-2888@v19.2.7#s3_expiration_header`); tentacle
       30daa120099; tracker #77564.
     - "Squid's lifecycle transition of an encrypted multipart object drops its part lengths":
       data corruption, the object cannot be decrypted; v19.2.6, v20.2.0 and v20.2.1; fixed in
       v19.2.7 by b9b65194f64
       (`src/rgw/driver/rados/rgw_rados.cc:5155-5182@v19.2.7#fixup_manifest_to_parts_len`) and
       in v20.2.2; tracker #23264.
     - The fourth, "Squid's DeleteObjects reports another key's result" (41984610868, tracker
       #76960), waits for F11 and is added only if the re-record shows a flip it explains.
  5. "rgw-go:" lines are left to F4 to F9, each of which owns its entry's.
- **Specs:** citecheck over the file with 0 failures; a grep for every changed title in
  `docs/exclusions.md`, the known-differences files and the registry index.
- **Exclusions and registry:** this task is the registry's facts.
- **Depends on:** nothing. **PR:** its own; shared with the rgw-rs session. **Brief:** about
  120 lines.

### Task F3: docs, the exclusions at v19.2.7

- **Layer:** docs. **Cluster:** no.
- **Files:** modify docs/exclusions.md; `test/s3tests/known-differences-squid.txt`'s comments
  only if a heading they name changes.
- **Behaviour** (report §4's rows marked F3; no rgw-go behaviour changes):
  1. "Write conditions are Tentacle's on Squid too" (:4225-4256) shrinks to its
     CompleteMultipartUpload bullet, a Squid difference against v19.2.7, which still reads
     neither condition
     (`src/rgw/rgw_rest_s3.cc:4094-4104@v19.2.7#RGWCompleteMultipart_ObjStore_S3::get_params`;
     Tentacle's takes both, `src/rgw/rgw_rest_s3.cc:4565@v20.2.4#RGWCompleteMultipart_ObjStore_S3::get_params`).
     Its "merged after v19.2.6" becomes "first tagged in v19.2.7". The heading stays, since
     `known-differences-squid.txt` names it, or both change together.
  2. "Delete conditions are Tentacle's, and a delete is guarded, on both releases"
     (:4257-4296) loses its two Squid-zone bullets: v19.2.7 checks the conditions as v20.2.4
     does (`src/rgw/rgw_rest_s3.cc:3435-3483@v19.2.7#RGWDeleteObj_ObjStore_S3::get_params`,
     `src/rgw/rgw_rest_s3.cc:3685-3733@v20.2.4#RGWDeleteObj_ObjStore_S3::get_params`). Its
     guard bullet covers both releases: both remove a replaced head unguarded.
  3. "Deletes that need MFA are refused" (:3610-3627): the inverted DeleteObjects check is on
     Squid 19.2.7 zones too.
  4. "UploadPart ignores If-Match and If-None-Match" (:4673): its Squid side is v19.2.7, which
     still passes none.
  5. "The metadata sections write only what keeps a user or bucket whole" (:1548-1559): "which
     neither release's JSON carries" becomes v19.2.7's form, with `obj_lock` in the document
     (`src/rgw/rgw_common.cc:2542-2544@v19.2.7`). F8 and F9 then edit the same bullet for
     rgw-go's side.
  6. The anchors follow F2's retitles.
- **Specs:** none; a grep that every heading `known-differences-*.txt` names still exists.
- **Exclusions and registry:** this task is the exclusions' facts.
- **Depends on:** F2. **PR:** its own; announced to the rgw-rs session. **Brief:** about 90
  lines.

### Task F4: auth, an unsigned Content-Type and x-amz-content-sha256, once shown safe

- **Layer:** auth. **Cluster:** no.
- **Files:** modify internal/auth/sigv4.go (`canonicalHeadersV4`), the `Insecure` comment in
  internal/auth/config.go (:25-29), the auth specs and vectors under internal/auth/testdata/v4/,
  both `test/s3tests/known-differences-*.txt`, the new exclusion, and the two registry entries'
  "rgw-go:" lines. On FD-4's fallback only: internal/auth/config.go's release,
  internal/cli/serve.go (one argument) and the test callers of `DefaultConfig`.
- **Consumes:** phase 1's verifier. **Produces:** the relaxed rule; FD-4's seam only on its
  fallback.
- **Behaviour:**
  1. **Precondition, the task's first step** (the owner, 2026-10-13, P5). Nothing else in the
     task starts until the task's notes hold all three:
     - (a) **The attack.** Read v20.2.4's CVE-2026-54330 hardening: 683fad73231, "rgw: Follow
       guidelines when verifying SigV4 signatures" (squid d8ba9d0cb06), and its companion
       3138362dbca, which adds `rgw_sigv4_insecure`, both merged into tentacle by 5978c866d4a
       from the advisory branch ceph-ghsa-rmjq-ffrm-j6vj. The check they add is
       `src/rgw/rgw_auth_s3.cc:765-797@v20.2.4`. State the attack that requiring a signed
       Content-Type and a signed x-amz-content-sha256 closes, citing the commit and the code.
       Upstream's own reading is that the escalation comes from unsigned `x-amz-*` headers,
       which v19.2.7 still refuses (`src/rgw/rgw_auth_s3.cc:816-817@v19.2.7`); the task
       verifies that reading against the advisory and the commits, and does not assume it.
     - (b) **rgw-go stays safe without the two requirements.** Show from rgw-go's code that
       whatever (a) found the CVE needs still holds. At least: the payload hash the body is held
       to is the one the signature covers, since `authDataV4` takes it once from
       `expectedPayloadHash` and uses it both as the canonical request's HashedPayload and for
       the body check (internal/auth/sigv4.go:423-429, internal/auth/payload.go:56), as radosgw
       does (`src/rgw/rgw_auth_s3.h:638-660@v19.2.7#get_v4_exp_payload_hash`,
       `src/rgw/rgw_auth_s3.h:641-664@v20.2.4#get_v4_exp_payload_hash`, used at
       `src/rgw/rgw_rest_s3.cc:5858@v19.2.7`); and every other `x-amz-*` header is still
       refused unsigned.
     - (c) **AWS's rule.** Read and quote, from AWS's documentation and not from memory, its
       SigV4 rule on which headers must be signed, with the URL. Both upstream commits name the
       page, `https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html`
       (`src/rgw/rgw_auth_s3.cc:789@v19.2.7`), and v19.2.7's comment records that the page
       contradicts itself on Content-Type (`src/rgw/rgw_auth_s3.cc:797-815@v19.2.7`), so the
       quotation gives every passage on Content-Type, `x-amz-*` headers and
       x-amz-content-sha256. Where the passages disagree, the record adds behavioural
       evidence: whether the AWS SDKs' own signers (botocore's and the Go SDK's, each cited at a
       release) sign the two headers, and what AWS S3 itself accepts. (c) is evidence for the
       owner. It does not decide the outcome.
     - (d) **If (a) and (b) show the relaxation reopens the attack,** the task keeps Tentacle's stricter check, v20.2.4's, and
       records why in its notes, the registry entries and, for any release where rgw-go then
       differs from its floor, an exclusion. Whether Squid then follows v19.2.7, which needs
       FD-4's release seam, or keeps the stricter check too is part of what the owner sees. The
       owner sees that outcome before the merge.
  2. **The rule, on both releases** (P5): v19.2.7's `get_v4_canonical_headers`
     (`src/rgw/rgw_auth_s3.cc:788-842@v19.2.7`). Unless `rgw_sigv4_insecure`, `host` and every
     `x-amz-*` header the request carries must be signed, except `x-amz-content-sha256`; a
     present Content-Type need not be. Every refusal is still AccessDenied.
  3. **Tentacle differs:** v20.2.4 also refuses an unsigned Content-Type and an unsigned
     x-amz-content-sha256 (`src/rgw/rgw_auth_s3.cc:765-797@v20.2.4`). rgw-go accepts them on
     Tentacle too, as an exclusion (P5). The tentacle branch carries both fixes
     through ceph/ceph#71364 (merge 7e2e2072822: a25fdb5267c and a1dd71be880), in no Tentacle tag yet.
  4. Both the header route and the presigned route take the rule, and so does the boto2
     fallback's second canonicalization.
- **Specs:**
  - **The precondition gate,** first and on its own: the task's notes hold (a), (b) and (c),
    each cited; the reviewer judges them before any other part of the PR, and the PR does not
    merge without them, or, on (d), without the owner having seen the outcome;
  - an unsigned `x-amz-content-sha256`, header-signed and presigned, is accepted, and a body or
    a header value that differs from the payload hash the signature covers is refused, which
    pins (b);
  - an unsigned Content-Type beside an unsigned `x-amz-acl` is refused (upstream's invariant,
    `src/test/rgw/test_rgw_auth_sigv4.cc:219@v19.2.7`), and any other unsigned `x-amz-*`
    header is refused;
  - the vector `unsigned-content-type-rejected` becomes `unsigned-content-type-accepted`, with
    its canonical request; a presigned request with an unsigned Content-Type is accepted;
  - `unsigned-x-amz-header-rejected`, `unsigned-host-rejected` and
    `insecure-allows-unsigned-x-amz` keep their outcomes;
  - on (d) only: the vectors run under the release each names, and the stricter release keeps
    today's refusals.
- **s3-tests:** test_sigv4_contenttype_unsigned, test_sigv4_presigned_contenttype_unsigned and
  test_sigv4_without_signed_content_sha256 now pass on rgw-go on both releases (FD-5):
  - `known-differences-tentacle.txt` gains the three, under the new exclusion, since v20.2.4's
    radosgw fails them;
  - `known-differences-squid.txt` gains them until F11, since the v19.2.6 ledger records
    radosgw failing them.
- **Exclusions and registry:** a new exclusion, "SigV4 accepts an unsigned Content-Type and
  x-amz-content-sha256 on Tentacle too", citing the precondition's findings. The "rgw-go:"
  lines of F2's two SigV4 entries: accepted on both releases, as v19.2.7 does.
- **Depends on:** F2, F3. **PR:** its own. **Brief:** about 170 lines. Adversarial review: the
  authentication boundary (threat model), with the precondition gate checked first.

### Task F5: driver, a re-sent completion answers the stored ETag on both releases

- **Layer:** driver. **Cluster:** no.
- **Files:** modify internal/driver/mp_complete.go (:286-293) and
  internal/driver/mp_complete_test.go (:461-478); the registry entry's "rgw-go:" line; the
  s3-test on the `rgw-go-s3tests` branch (FD-9).
- **Behaviour:**
  1. `check_previously_completed` sets `etag = oetag` at both floors
     (`src/rgw/rgw_op.cc:6584@v19.2.7`, `src/rgw/rgw_op.cc:7472@v20.2.4`), so a retried
     completion of a finished upload answers the object's ETag. The release split goes.
  2. Size, mtime, the epoch and the unknown-id path are unchanged.
- **Specs:** the spec at mp_complete_test.go:461 expects the stored ETag on Squid and Tentacle
  alike.
- **s3-tests:** a new test on jhoblitt/ceph's `rgw-go-s3tests` branch: complete an upload, send
  the same CompleteMultipartUpload again, and expect 200 with the object's ETag. It enters the
  ledgers at a later pin bump; F11 does not move the pin.
- **Exclusions and registry:** none; the entry's "rgw-go:" line says rgw-go answers the stored
  ETag on both, as both floors do.
- **Depends on:** F2. **PR:** shared with F6, the same behaviour; two commits. **Brief:** about
  50 lines.

### Task F6: memstore, the same for the in-memory store

- **Layer:** memstore. **Cluster:** no.
- **Files:** modify internal/memstore/multipart.go (`previouslyCompleted`, :245-272, and its
  comment) and its spec (:347); internal/s3/multipart_test.go:889-913, whose Squid entry
  expects "".
- **Behaviour:** as F5. `previouslyCompleted` keeps the stored ETag below Tentacle too.
- **Specs:** both table entries expect the stored ETag; the memstore spec at :347 likewise.
- **Exclusions and registry:** none beyond F5's line.
- **Depends on:** F2. **PR:** with F5. **Brief:** about 35 lines.

### Task F7: driver, a reshard guard that meets a missing shard re-reads the bucket and retries

- **Layer:** driver. **Cluster:** no; its integration spec runs in the nightly workflow and in
  F11's cluster run.
- **Files:** modify internal/driver/indexop.go (`guardReshard`, :221-252, and
  `refreshIndexOp`), internal/driver/indexop_test.go, test/integration/write_test.go:403-412,
  and the registry and exclusions paragraphs for this behaviour.
- **Consumes:** phase 1's `refreshIndexOp`. **Produces:** the retry, for every index call.
- **Behaviour:**
  1. **v19.2.7's guards** retry an `-ENOENT` within NUM_RESHARD_RETRIES, ten
     (`src/rgw/driver/rados/rgw_rados.cc:7225-7230@v19.2.7`, the bound at
     `src/rgw/driver/rados/rgw_rados.cc:7216@v19.2.7`; `RGWRados::guard_reshard` at
     `src/rgw/driver/rados/rgw_rados.cc:7929-7933@v19.2.7`). The retry re-reads the bucket:
     `init_bs` passes the target's bucket info
     (`src/rgw/driver/rados/rgw_rados.h:947-955@v19.2.7`), and that `BucketShard::init`
     overload reads the instance into it
     (`src/rgw/driver/rados/rgw_rados.cc:2804-2830@v19.2.7`). **v20.2.4 differs:** its guard
     breaks on any answer but busy-resharding
     (`src/rgw/driver/rados/rgw_rados.cc:7888-7891@v20.2.4`).
  2. **rgw-go:** an index call answering ErrNotFound calls `refreshIndexOp`, which reads the
     instance from RADOS (FD-6). When the re-read instance puts the key on another shard object,
     the call is retried on it, within the same ten calls that busy-resharding retries count.
     When it names the same shard object, the ErrNotFound is returned at once: a write that
     meets it answers 404 NoSuchKey, as now.
  3. A bucket removed meanwhile answers 404 NoSuchKey, as `refreshIndexOp` already maps it.
  4. **Who takes it:** the prepare (`src/rgw/driver/rados/rgw_rados.cc:7289-7291@v19.2.7`), the
     cancel (`src/rgw/driver/rados/rgw_rados.cc:7395-7397@v19.2.7`) and the completion
     manager's retry (`src/rgw/driver/rados/rgw_rados.cc:905@v19.2.7`), and phase 2's OLH ops
     (`src/rgw/driver/rados/rgw_rados.cc:8153@v19.2.7`,
     `src/rgw/driver/rados/rgw_rados.cc:8207@v19.2.7`), which V5 puts on `guardReshard`.
  5. **On Tentacle too** (P5), as an exclusion against v20.2.4's 404.
- **Specs** (fakerados; a BeforeWrite hook on the old shard commits a reshard):
  - a prepare whose shard a committed reshard removed lands on the new shard, and the write
    succeeds;
  - a completion retry does the same;
  - ENOENT with the same layout returns ErrNotFound after one call;
  - a bucket deleted before the retry answers NoSuchKey;
  - busy-resharding and ENOENT retries share the ten-call bound;
  - write_test.go:403-412 expects no stale-shard failure, with its tolerance and re-send
    removed.
- **Exclusions and registry:** "Dynamic resharding worker" (docs/exclusions.md:262-318) gains
  the missing-shard retry, a Tentacle difference from v20.2.4 (P5). The registry entry's
  "rgw-go:" line: no longer reproduced, on either release.
- **Depends on:** F2, F3. **PR:** its own. **Brief:** about 140 lines.

### Task F8: meta, RGWObjectLock typed, and the bucket instance JSON carries obj_lock

- **Layer:** meta. **Cluster:** no.
- **Files:** modify internal/meta/objectlock.go (the lock types, FD-7),
  internal/meta/bucket_info.go (`MarshalJSON`, :326-385), internal/meta/bucket_json.go
  (`UnmarshalJSON`), and their specs; the exclusions bullet for the Tentacle difference.
- **Produces:** the lock types, `ObjectLock()`, `SetObjectLock()`, and `UnmarshalJSON`'s
  presence rule (Seams).
- **Behaviour:**
  1. **The encoding:** RGWObjectLock is `ENCODE_START(1, 1)` of `enabled`, `rule_exist` and,
     when the rule exists, ObjectLockRule wrapping DefaultRetention (`mode`, `days`, `years`),
     each `ENCODE_START(1, 1)` (`src/rgw/rgw_object_lock.h:34-40@v19.2.7`,
     `src/rgw/rgw_object_lock.h:127-135@v19.2.7`; the same at
     `src/rgw/rgw_object_lock.h:33-39@v20.2.4`, `src/rgw/rgw_object_lock.h:123-131@v20.2.4`).
  2. **`ObjectLock()`** decodes `ObjLock` when the flags carry BUCKET_OBJ_LOCK_ENABLED: nil is
     RGWObjectLock's default, enabled with no rule; without the flag it is the disabled zero
     value. **`SetObjectLock`** encodes into `ObjLock` and leaves the flags to the caller. An
     `ObjLock` nobody sets keeps its bytes, so a stored rule re-encodes exactly.
  3. **The dump:** with the flag, `obj_lock` follows `sync_policy`
     (`src/rgw/rgw_common.cc:2538-2544@v19.2.7#RGWBucketInfo::dump`), in v19.2.7's form:
     `enabled` only when true, then `rule_exist`, then `rule` when it exists, holding
     `defaultRetention` with `mode` and `days` when positive, else `years`
     (`src/rgw/rgw_object_lock.cc:93-101@v19.2.7#RGWObjectLock::dump`,
     `src/rgw/rgw_object_lock.cc:56-58@v19.2.7#ObjectLockRule::dump`,
     `src/rgw/rgw_object_lock.cc:26-33@v19.2.7#DefaultRetention::dump`). An `ObjLock` that
     does not decode fails `MarshalJSON` with its error.
  4. **The decode:** `obj_lock` is read only under the document's flag
     (`src/rgw/rgw_common.cc:2588-2590@v19.2.7`), by v19.2.7's decoders
     (`src/rgw/rgw_object_lock.cc:65-71@v19.2.7#RGWObjectLock::decode_json`,
     `src/rgw/rgw_object_lock.cc:44-46@v19.2.7#ObjectLockRule::decode_json`,
     `src/rgw/rgw_object_lock.cc:8-12@v19.2.7#DefaultRetention::decode_json`): `rule` only when
     `rule_exist`, and a missing member reads as its zero value, as `T()`
     (`src/common/ceph_json.h:334-346@v19.2.7`). A present `obj_lock` sets `ObjLock` to its
     encoding, the default included; an absent one leaves it nil, which F9 reads.
  5. **v20.2.4 differs:** its dump and decode carry no `obj_lock`
     (`src/rgw/rgw_common.cc:2577-2604@v20.2.4#RGWBucketInfo::dump`,
     `src/rgw/rgw_common.cc:2606-2647@v20.2.4#RGWBucketInfo::decode_json`), and its
     RGWObjectLock dump keeps the old form (`src/rgw/rgw_object_lock.cc:77@v20.2.4#RGWObjectLock::dump`).
     rgw-go writes and reads v19.2.7's form on Tentacle too (P5): a v20.2.4 radosgw ignores
     the key.
  6. Nothing changes a put yet: `keepInstance` and `newInstance` still overwrite `ObjLock`
     until F9.
- **Specs:**
  - round trips, byte-identical, of the default, a GOVERNANCE days rule and a COMPLIANCE years
    rule;
  - `ObjectLock()` without the flag, with the flag and nil, and with a rule;
  - the dump with and without the flag, against expected JSON built by hand from the v19.2.7
    functions above, since no corpus instance has the flag;
  - the decode with `obj_lock` absent (nil), present as the default (non-nil), present with a
    rule, and present without the flag (ignored);
  - the corpus goldens unchanged.
- **Exclusions and registry:** a new bullet, "The bucket instance document carries `obj_lock` on
  Tentacle too" (P5); the registry entry's "rgw-go:" line names the JSON form.
- **Depends on:** F2, F3. **PR:** its own. **Brief:** about 130 lines.

### Task F9: driver, a bucket instance put takes a document's obj_lock

- **Layer:** driver. **Cluster:** no.
- **Files:** modify internal/driver/metadata.go (`keepInstance` :582-605, `newInstance`
  :609-630) and its specs; the memstore's metadata put where it mirrors the driver's; the
  exclusions bullet and the registry's "rgw-go:" line.
- **Consumes:** F8's presence rule and `SetObjectLock`.
- **Behaviour:**
  1. With BUCKET_OBJ_LOCK_ENABLED set and an `obj_lock` in the document, the document's
     configuration is stored, for a new instance and an existing one, as v19.2.7's decode
     yields it (`src/rgw/rgw_common.cc:2588-2590@v19.2.7`) and its put stores the decoded
     instance whole
     (`src/rgw/driver/rados/rgw_bucket.cc:2866-2887@v19.2.7#RGWMetadataHandlerPut_BucketInstance::put_checked`).
  2. With no `obj_lock`, an existing instance keeps its stored configuration, where v19.2.7
     resets it to RGWObjectLock() (`src/common/ceph_json.h:334-346@v19.2.7`) (FD-8). A new
     instance gets the default, as now.
  3. The refusal to turn object lock off stays.
  4. **v20.2.4 differs:** it never reads `obj_lock`, so its put always drops the rule (the
     registry's #71157). rgw-go takes the document's on Tentacle too (P5).
- **Specs:** a put with a changed rule stores it; a put without `obj_lock` over a ruled
  instance keeps the rule; a new instance with a rule; the refusal unchanged; the memstore
  likewise where it mirrors.
- **Exclusions and registry:** "The metadata sections write only what keeps a user or bucket
  whole" (:1548-1559): the document's `obj_lock` when present, the stored one when absent, a
  difference from both floors. The registry entry's "rgw-go:" line follows.
- **Depends on:** F8. **PR:** its own. **Brief:** about 90 lines.

### Task F10: hack/goldens, v19.2.7's ceph-dencoder by default

- **Layer:** hack. **Cluster:** no; it needs podman and the image.
- **Files:** modify hack/goldens/gen.sh (:5, :15).
- **Behaviour:**
  1. The default image becomes `quay.io/ceph/ceph:v19.2.7`.
  2. A regeneration over hack/goldens/types.txt changes no golden. No encoding changed between
     the tags; the only dump change, RGWBucketInfo's `obj_lock`, needs BUCKET_OBJ_LOCK_ENABLED,
     which no corpus instance has (flags 0, 1 and 2); RGWObjectLock is not in types.txt.
- **Specs:** `git diff --exit-code` over the testdata/goldens trees after `hack/goldens/gen.sh`.
  A diff stops the task: it is a finding against the report.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** its own. **Brief:** about 25 lines.

### Task F11: ops and parity, Squid's pin bumped and radosgw's Squid ledgers re-recorded

- **Layer:** ops and parity records. **Cluster:** yes, Squid, one at a time.
- **Files:**
  - the pin: hack/rooket/squid/values/rook-ceph-cluster.yaml:5 (`tag: v19.2.7`, the one home of
    Squid's image), .goreleaser.yaml:13 (`SQUID_CEPH_TAG`), the comment at
    .github/actions/rooket-cluster/action.yml:48-50, the help example at Makefile:219,
    hack/rooket/README.md:11;
  - the fixture: test/integration/write_test.go:273-291;
  - the records: test/s3tests/baseline/squid.json, test/admin/baseline/squid.json,
    test/s3tests/known-differences-squid.txt.
- **Behaviour:**
  1. **First,** check that `quay.io/ceph/ceph:v19.2.7` is published; the sandbox could not
     reach quay.io, so the report leaves it unverified. hack/rooket/lib.sh needs no change: it
     derives the version from the tag (hack/rooket/lib.sh:61-69).
  2. `make cluster-up RELEASE=squid`, `make populate`, `make gate RELEASE=squid`, `make
     rgw-go-up`.
  3. **Record both ledgers in full** at `ceph_version` 19.2.7, `make s3tests-record
     RELEASE=squid` and `make admin-record RELEASE=squid`, appended, editing no entry. A
     version bump records every test again; neither is a subset record.
  4. **Each changed outcome** carries a REASONS line naming its v19.2.7 commit: ce98cebb815 for
     the eight condition tests, eb63ba5d7de and 1db3ab3b185 for the three SigV4 tests. Any
     other flip is investigated before it is reasoned; an unexplained one stops the task. A
     flip that 41984610868 explains gets F2's conditional registry entry in a follow-up docs PR.
  5. **known-differences-squid.txt** loses the eight condition lines and F4's three SigV4
     lines. It keeps test_multipart_put_object_if_match, the four GetObjectAttributes tests and
     test_expected_bucket_owner; any of them that now agrees is investigated.
  6. **The fixture:** write_test.go's Squid branch of "answers the write conditions as radosgw
     does" asserted v19.2.6's 412, 412, 412; at v19.2.7 radosgw answers v20.2.4's 404, 200,
     200, so it collapses into the Tentacle branch. It changes here because each expectation
     fails against the other release's cluster.
- **Checks:** `make ledger-check`; `make s3tests-parity RELEASE=squid` and `make admin-parity
  RELEASE=squid`, both clean; `make integration RELEASE=squid`, with the collapsed conditions
  and F7's tightened reshard spec; `make release-pins-check` (hack/image/release-pins-check.sh
  fails when the values file and .goreleaser.yaml disagree). Watch the PR's s3-tests check.
- **Exclusions and registry:** none of its own; F2's conditional entry, if any, follows.
- **Depends on:** F1, F3, F4, F10; after F5 to F7, so the integration run covers them. It is the
  first ledger append after the floor moves, so it lands before every phase 2 recording task.
- **PR split:** one PR (FD-3), two commits: (1) the pin, the CI comment, the Makefile, the
  README and the fixture; (2) the two ledgers and known-differences-squid.txt. **Brief:** about
  150 lines.

### Task F12: driver, guardHead's instance exemption on both releases

Found by F3's task review (Important 1), and numbered here before F3 merged (PP-12).

- **Layer:** driver. **Cluster:** no.
- **Files:** modify internal/driver/headwrite.go (`guardHead`, its release test) and its specs.
- **Behaviour:**
  1. radosgw sets the head write's id-tag guard only for a key with no instance and a request
     without `If-None-Match: *`, the same at both tags
     (`src/rgw/driver/rados/rgw_rados.cc:3202@v19.2.7`, `src/rgw/driver/rados/rgw_rados.cc:3299@v20.2.4`).
  2. rgw-go keeps v19.2.6's Squid guard for a versioned instance: `guardHead` exempts an
     instance only when the release is Tentacle. F12 drops that release test, so an instance's
     head write takes no id-tag cmpxattr on either release.
  3. Today the branch is unreachable, since versioned writes answer 501 until V11. F12 lands
     before V7a, which writes versioned instances.
- **Specs:** the guard table on both releases. A key with no instance is guarded; an instance is
  not; `If-None-Match: *` is not.
- **Exclusions and registry:** none. The split was an unrecorded difference, and F12 removes it.
- **Depends on:** F3. **PR:** its own. **Brief:** about 40 lines.

### Task F13: auth, every prefix init_meta_info renames must be signed

Found in F4's work, and numbered here before F4 merged (PP-12).

- **Layer:** auth. **Cluster:** no; its s3-test runs in CI.
- **Files:** modify internal/auth/sigv4.go (`canonicalHeadersV4`) and its specs; docs/exclusions.md;
  docs/ceph-upstream-bugs.md (the entry's "rgw-go:" line).
- **Behaviour:**
  1. radosgw's CVE-2026-54330 check refuses an unsigned header only under `HTTP_X_AMZ_`
     (`src/rgw/rgw_auth_s3.cc:819-841@v19.2.7`, `src/rgw/rgw_auth_s3.cc:780-796@v20.2.4`).
     `init_meta_info` then renames six more prefixes onto `x-amz-*` names, into `x_meta_map` and
     `crypt_attribute_map` (`src/rgw/rgw_common.cc:413-464@v19.2.7`,
     `src/rgw/rgw_common.cc:426-477@v20.2.4`). So an unsigned `X-Goog-Meta-*` is stored as user
     metadata, and an unsigned `X-Goog-Server-Side-Encryption-*` reaches the SSE attributes.
  2. rgw-go maps the same prefixes, and has the same gap. Unless insecure, it refuses an unsigned header
     under any prefix that init_meta_info renames, exactly as it refuses an unsigned `x-amz-*`, on both
     releases. That closes the class the CVE fixed. The x-amz-content-sha256 exemption (F4) stays
     limited to that one header name.
  3. The check's prefix set is init_meta_info's. A spec pins it to the tables rgw-go already keeps,
     so the three cannot drift.
  4. SigV2 is unchanged: its string to sign already covers `x_meta_map`
     (`src/rgw/rgw_auth_s3.cc:248@v20.2.4`).
- **Specs:** each of the six prefixes, unsigned, is refused, on the header route and the presigned route;
  each one signed is accepted; an unsigned `x-amz-*` is still refused, and x-amz-content-sha256 still
  passes; insecure accepts all of them.
- **s3-tests:** a presigned PUT with an unsigned `X-Goog-Meta-Foo` answers 403, merged into
  jhoblitt/ceph's `rgw-go-s3tests` branch, with the known-differences lines that radosgw's answer needs.
- **Exclusions and registry:** a new exclusion, "rgw-go refuses an unsigned header that radosgw renames
  to x-amz-*", on both releases. The registry entry's "rgw-go:" line names the fix.
- **Depends on:** F4, which changes the same loop. **PR:** its own. **Brief:** about 50 lines.

### Task F14: op, a CompleteMultipartUpload naming a version answers 501

Found by F12's task review (Important 1), and numbered here before F12 merged (PP-12).

- **Layer:** op. **Cluster:** no.
- **Files:** modify internal/op/completemultipart.go and its specs; docs/ceph-upstream-bugs.md (the
  entry for `versionId` honoured on a bucket not versioning-enabled).
- **Behaviour:**
  1. rgw-go reads `?versionId` into the object's instance for every op
     (internal/s3/request.go:100). PutObject and CopyObject refuse a write that names a version with
     501 (`newHeadVersioningUnserved`, internal/op/putobject.go:147, internal/op/copyobject.go:203);
     CompleteMultipartUpload has no such gate (internal/op/completemultipart.go:66-95). So on a bucket
     never versioned, a completion naming `null` indexes the plain head under the null instance, and
     one naming another id writes that instance without radosgw's versioned op.
  2. CompleteMultipartUpload calls `newHeadVersioningUnserved` with the request's key before it
     touches the store, as PutObject does, and answers the same 501. The upload stays in place.
  3. radosgw takes the URL's instance unless versioning is enabled
     ([rgw_op.cc:6462-6469](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6462-L6469),
     [rgw_op.cc:7284-7291](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7284-L7291)),
     so the registry entry for PutObject's `versionId` covers the completion too; its "rgw-go:" line
     names F14's refusal. Phase 2 replaces the 501 with D3, which now names the completion (V11).
- **Specs:** on an unversioned bucket, a completion naming `null` and one naming another id answer
  501 and leave the upload listable; one without `versionId` completes as before.
- **s3-tests:** none. The 501 is phase 1's refusal, which V11 replaces with D3, and V11's s3-tests
  cover D3.
- **Exclusions and registry:** the registry entry's text and "rgw-go:" line; exclusions.md's
  versioned-write refusal already says rgw-go answers 501, which F14 makes true for the completion.
- **Depends on:** nothing. **PR:** its own. **Brief:** about 35 lines.

### Task F15: auth, SigV2 refuses a renamed header carrying an x-amz directive

Found by F13's adversarial review (Minor 5), and numbered here before F13 merged (PP-12).

- **Layer:** auth. **Cluster:** no.
- **Files:** modify internal/auth/sigv2.go and its specs; docs/exclusions.md; the "rgw-go:" line of
  F13's registry entry for the SigV2 relabelling.
- **Behaviour:**
  1. SigV2's string to sign builds its canonical amz headers from `x_meta_map`
     ([rgw_auth_s3.cc:246](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L246)), where `init_meta_info` has renamed
     X-Goog-, X-Dho-, X-Rgw-, X-Object-, X-Container- and X-Account- onto `x-amz-*` names, while
     radosgw reads its directives from the raw headers: tagging
     ([rgw_rest_s3.cc:2634](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2634)), object lock
     ([rgw_rest_s3.cc:2648](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2648)) and copy source
     ([rgw_rest_s3.cc:5056](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5056)). So a signed `X-Amz-Copy-Source` resent as
     `X-Goog-Copy-Source` keeps the signature and drops the directive. rgw-go copies the
     construction.
  2. Under SigV2, rgw-go refuses with AccessDenied, on both releases, a header under a renamed prefix
     whose `x-amz-*` name rgw-go reads only from the raw header: every name but the meta and the
     server-side-encryption ones that the renamed maps carry. A spec pins the refused set to every
     raw `x-amz-*` read in rgw-go's handlers, so a new read cannot be missed.
  3. Meta under a renamed prefix still works under SigV2.
- **Specs:** copy source, the object lock headers and tagging under X-Goog-, each refused;
  X-Goog-Meta-* accepted; the raw X-Amz-* forms unchanged; every prefix.
- **s3-tests:** a SigV2 request resending a signed `X-Amz-Copy-Source` as `X-Goog-Copy-Source`
  answers 403, if s3-tests can sign SigV2 that way; otherwise the report says why not.
- **Exclusions and registry:** a new exclusion (rgw-go refuses where radosgw drops the directive);
  the registry entry's "rgw-go:" line.
- **Depends on:** F13. **PR:** its own. **Brief:** about 45 lines.

## Order

| Wave | Tasks | Waits for |
|---|---|---|
| 1, in parallel | F1, F2, F10 | nothing |
| 2 | F3 | F2 |
| 3, in parallel | F4, F5 with F6, F7, F8, F12, F13, F14, F15 | F2 and F3 (F5 and F6 only F2; F13 after F4; F14 nothing; F15 after F13) |
| 4 | F9 | F8 |
| cluster, one at a time | F11 | F1, F3, F4, F10; after F5 to F7 |
| then | the deferred benchmark run (spec §9), then phase 2's S1 to S3 | every F task merged |

F11 is the plan's only cluster task in this unit; it takes the one-at-a-time cluster slot and the
controller's recording slot. F4 gets an adversarial review; no F task is an OLH driver task, so
none takes P3's Fable review.

## Defects

The report's registry changes (§4), with the task that carries each. Status stays unreproduced
until a running release shows the defect (PP-11); F11's re-record shows v19.2.7's fixes on a
running cluster for the tests it reasons.

| # | Defect | Releases | Class | Status | rgw-go's choice | Task |
|---|---|---|---|---|---|---|
| FX-1 | SigV4 refuses an unsigned Content-Type (registry) | v20.2.4 (and v19.2.6); fixed in v19.2.7, eb63ba5d7de | S3-correctness, fails closed | as the registry records it | accepted on both, as v19.2.7, once F4's precondition holds; a Tentacle exclusion (the owner, 2026-10-13, P5) | F2, F4 |
| FX-2 | SigV4 refuses an unsigned x-amz-content-sha256 (new) | v20.2.4 (and v19.2.6); fixed in v19.2.7, 1db3ab3b185; not on the tentacle branch | S3-correctness, fails closed | unreproduced | as FX-1 | F2, F4 |
| FX-3 | A re-sent CompleteMultipartUpload answers an empty ETag (registry) | v19.2.6 only; fixed in v19.2.7, 4b567265365 | S3-correctness | as recorded | the stored ETag on both | F2, F5, F6 |
| FX-4 | x-amz-expiration reports a NoncurrentVersionExpiration rule (new) | v20.2.4 (and v19.2.6); fixed in v19.2.7, 4dac1850993; tentacle 30daa120099, no tag | S3-correctness | unreproduced | C-D7's form on both; a Tentacle exclusion (P5) | F2; C2 |
| FX-5 | A lifecycle transition of an encrypted multipart object drops its part lengths (new) | v19.2.6, v20.2.0, v20.2.1; fixed in v19.2.7, b9b65194f64, and v20.2.2 | data corruption | unreproduced | C11 writes the part lengths as both floors do; objects v19.2.6 transitioned stay damage rgw-go cannot repair | F2; C11 |
| FX-6 | A write whose bucket was resharded since the request read it fails 404 (registry) | v20.2.4 (and v19.2.6); fixed in v19.2.7, 596eaf34725 | S3-correctness | as recorded | retry on both, re-reading from RADOS (FD-6); a Tentacle exclusion (P5) | F2, F7 |
| FX-7 | A bucket instance metadata put drops the object lock rule (registry), with v19.2.7's residual: a document without `obj_lock` still resets it | v20.2.4 (and v19.2.6); the residual at v19.2.7 | as recorded (weakens a WORM bucket) | unreproduced, derived from the source | v19.2.7's JSON on both (F8); the stored rule kept when absent (F9, FD-8); exclusions | F2, F8, F9 |
| FX-8 | PUT refuses a quoted If-Match, and answers 412 for one on a missing key (registry) | quoting and the 412 v19.2.6 only, fixed by ce98cebb815; the prefix comparison at both floors | S3-correctness | as recorded | v20.2.4's conditions on both, phase 1's, unchanged | F2, F3 |
| FX-9 | Write conditions fail an If-None-Match ETag on a missing key and skip a head without a write tag (registry) | v19.2.6 only | S3-correctness | as recorded | unchanged | F2 |
| FX-10 | DeleteObject removes the head without checking its write tag (registry, retitled) | v19.2.7, v20.2.4, main | as recorded | as recorded | the guard kept on both (phase 1's exclusion) | F2, F3 |
| FX-11 | DeleteObjects asks for MFA for the keys that need none and not for the versions (registry, retitled) | v19.2.7, v20.2.1 to v20.2.4 | security (MFA bypass) | unreproduced | main's check on both | F2, F3 |
| FX-12 | UploadPart fails with If-None-Match: * (registry) | v20.2.4; v19.2.7 still ignores the conditions | as recorded | as recorded | unchanged | F2 |
| FX-13 | radosgw faults on a zero `rgw_gc_max_objs` (registry): its runtime-change crash | now kept from happening at both floors by the `startup` flag | as recorded | as recorded | unchanged; #75958 not asserted closed | F2 |
| FX-14 | DeleteObjects reports another key's result (conditional, 41984610868, #76960) | v19.2.6 only | S3-correctness | added only if F11's re-record shows a flip it explains | each key's own result, phase 1's | F11, then a docs PR |
