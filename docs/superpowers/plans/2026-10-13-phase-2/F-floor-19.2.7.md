# Phase 2 unit F: the Squid floor at v19.2.7

Threat model: F4 sits at the spec's authentication boundary and defends against a client
adversary: which headers a SigV4 signature must cover, the CVE-2026-54330 hardening. It gets
adversarial review (PP-14). Every other task defends against honest mistakes: a bucket instance
document that loses an object lock rule (F8, F9), an index write that meets a reshard (F7), a
stale ledger, pin or registry fact (F1 to F3, F10, F11). F21 to F26, numbered from the
authentication sweep of 2026-11-21, sit at the same boundary: what a signature covers of the
query, the admin API's arguments and the body. F27 defends an administrator's tools against
bytes a bucket's tagger chose. Each of the seven gets adversarial review. F28 and F29 bound what
a holder of an admin cap can make the gateway allocate, and get the standard review. F30 and
F31, numbered from the frontend sweep of 2026-11-28, sit at the frontend, the first code an
untrusted byte reaches: the requests net/http answers or frames before any rgw-go handler runs.
Both get adversarial review. F32 changes no behaviour and gets the standard review.

Source: `research/research-floor-19.2.7.md`, with
task ids F1 to F11 kept. rgw-go paths are at origin/main 0239625. Line numbers in
`docs/ceph-upstream-bugs.md` and `docs/exclusions.md` are at that commit too. Where this file
departs from the report, the drafting notes says why.

## Scope

The owner approved on 2026-10-13 moving the Squid floor of spec §3 ("Ceph floor Squid 19.2.6 or
Tentacle 20.2.4") from v19.2.6 to v19.2.7 ([CMakeLists.txt:3-4](https://github.com/ceph/ceph/blob/v19.2.7/CMakeLists.txt#L3-L4), the version commit
9ec952b9f96, tagged 2026-10-08). Tentacle stays at v20.2.4 ([CMakeLists.txt:3-4](https://github.com/ceph/ceph/blob/v20.2.4/CMakeLists.txt#L3-L4)).
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
| What a signature covers: the query SigV4 signs and dispatch reads, the keys and arguments SigV2 leaves out, the order of an admin query, the body under a Content-MD5 (the sweeps of 2026-11-21) | F21 to F26 |
| The admin API's output and bounds: a tag key in bucket stats, a new instance's shard count, the usage listing (the sweeps of 2026-11-21) | F27 to F29 |
| The frontend, before any handler: the reads net/http makes with no deadline and `OPTIONS *`, a request that names two framings, and the answers and rewrites that are recorded (the frontend sweep of 2026-11-28) | F30 to F32 |

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

The unit carries the owner's decision P5. The unit's own decisions are FD-1 to FD-9. FD-10 is
open: it is the owner's to rule, and F22's header route and F23 wait for it.

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
| FD-10 (open: the owner's ruling) | A header-signed SigV2 request whose signature leaves out the query key that chooses its op (F22) or the arguments of an admin request (F23) is: (a) refused; (b) refused unless it arrived over TLS at rgw-go's own listener; or (c) served as now, with the registry's rgw-go lines saying rgw-go reproduces the defect. A presigned SigV2 request of either kind is refused under (a) and (b), since its holder is not its signer. Recommended: (b). | The Ceph dashboard signs its S3 and admin calls with SigV2 and names such keys ([awsauth.py:43-82](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/awsauth.py#L43-L82), [awsauth.py:43-82](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/awsauth.py#L43-L82); F22 and F23 cite its requests), and the spec keeps "every admin endpoint the Rook operator and the Ceph dashboard call" (`docs/exclusions.md:509-510`). No fix makes SigV2 cover what it leaves out without refusing a client that signs as the dashboard does. (a) ends the dashboard's management of rgw-go until its signer changes. (b) ends it on a plain-HTTP gateway alone, and leaves no third party holding a request it can alter: an observer cannot read TLS, and a presigned URL is refused. (c) leaves tracker [#23182](https://tracker.ceph.com/issues/23182) open on rgw-go. go-ceph's admin client, which Rook uses, signs SigV4 and is unaffected by any outcome. | F22, F23 |

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
| `docs/differences.md` | F3, F4, F7, F8, F9 | F3 first, every fact; then each task only its own bullet. Announced to the rgw-rs session. |
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
     ([CMakeLists.txt:3-4](https://github.com/ceph/ceph/blob/v19.2.7/CMakeLists.txt#L3-L4)).
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
       ([ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346)).
     - "radosgw faults on a zero rgw_gc_max_objs ..." (:550): the runtime-change crash is kept
       from happening by the `startup` flag at both floors
       ([rgw.yaml.in:1713-1714](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L1713-L1714),
       [rgw.yaml.in:1795-1796](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1795-L1796)). #75958 is not asserted closed.
  2. **Carried by v19.2.7 too**, retitled away from "Tentacle's", with their index rows and the
     anchors `docs/differences.md` and the known-differences comments use:
     - "Tentacle's DeleteObject removes the head without checking its write tag" (:7067):
       Releases v19.2.7, v20.2.4 and main
       ([rgw_rados.cc:6049-6057](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L6049-L6057)).
     - "Tentacle's DeleteObjects asks for MFA for the keys that need none ..." (:8557, index
       :192): Releases v19.2.7 and v20.2.1 to v20.2.4 ([rgw_op.cc:7067-7081](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L7067-L7081),
       [rgw_op.cc:7969-7983](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7969-L7983)); drop "No v19.2.x tag contains ce98cebb815".
     - "Tentacle fails an UploadPart that sends If-None-Match: * ..." (:7231): Releases v20.2.4;
       v19.2.7 still ignores the conditions.
  3. **The nine entries v19.2.7 still carries** gain v19.2.7 in their Releases lines (report §2,
     "Registry entries v19.2.7 still carries"; their code is unchanged between the tags).
  4. **New entries**, each unreproduced and derived from the source:
     - "radosgw 19.2.6 and 20.2.4 reject a SigV4 request whose x-amz-content-sha256 is
       unsigned": S3-correctness, fails closed; fixed in v19.2.7 by 1db3ab3b185
       ([rgw_auth_s3.cc:788-842](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L788-L842), [rgw_auth_s3.cc:765-797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L765-L797));
       trackers #79803 and #79873; ceph/ceph#71292 (main) and ceph/ceph#71360 (squid);
       the tentacle branch carries it too, through ceph/ceph#71364 (merge 7e2e2072822, a1dd71be880), in no Tentacle tag yet.
     - "radosgw's x-amz-expiration reports a NoncurrentVersionExpiration rule": S3-correctness;
       v19.2.6 and v20.2.4 ([rgw_lc.cc:2913-2928](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_lc.cc#L2913-L2928)); fixed in v19.2.7 by
       4dac1850993 ([rgw_lc.cc:2750-2888](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_lc.cc#L2750-L2888)); tentacle
       30daa120099; tracker #77564.
     - "Squid's lifecycle transition of an encrypted multipart object drops its part lengths":
       data corruption, the object cannot be decrypted; v19.2.6, v20.2.0 and v20.2.1; fixed in
       v19.2.7 by b9b65194f64
       ([rgw_rados.cc:5155-5182](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L5155-L5182)) and
       in v20.2.2; tracker #23264.
     - The fourth, "Squid's DeleteObjects reports another key's result" (41984610868, tracker
       #76960), waits for F11 and is added only if the re-record shows a flip it explains.
  5. "rgw-go:" lines are left to F4 to F9, each of which owns its entry's.
- **Specs:** citecheck over the file with 0 failures; a grep for every changed title in
  `docs/differences.md`, the known-differences files and the registry index.
- **Exclusions and registry:** this task is the registry's facts.
- **Depends on:** nothing. **PR:** its own; shared with the rgw-rs session. **Brief:** about
  120 lines.

### Task F3: docs, the exclusions at v19.2.7

- **Layer:** docs. **Cluster:** no.
- **Files:** modify docs/differences.md; `test/s3tests/known-differences-squid.txt`'s comments
  only if a heading they name changes.
- **Behaviour** (report §4's rows marked F3; no rgw-go behaviour changes):
  1. "Write conditions are Tentacle's on Squid too" (:4225-4256) shrinks to its
     CompleteMultipartUpload bullet, a Squid difference against v19.2.7, which still reads
     neither condition
     ([rgw_rest_s3.cc:4094-4104](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4094-L4104);
     Tentacle's takes both, [rgw_rest_s3.cc:4565](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L4565)).
     Its "merged after v19.2.6" becomes "first tagged in v19.2.7". The heading stays, since
     `known-differences-squid.txt` names it, or both change together.
  2. "Delete conditions are Tentacle's, and a delete is guarded, on both releases"
     (:4257-4296) loses its two Squid-zone bullets: v19.2.7 checks the conditions as v20.2.4
     does ([rgw_rest_s3.cc:3435-3483](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3435-L3483),
     [rgw_rest_s3.cc:3685-3733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3685-L3733)). Its
     guard bullet covers both releases: both remove a replaced head unguarded.
  3. "Deletes that need MFA are refused" (:3610-3627): the inverted DeleteObjects check is on
     Squid 19.2.7 zones too.
  4. "UploadPart ignores If-Match and If-None-Match" (:4673): its Squid side is v19.2.7, which
     still passes none.
  5. "The metadata sections write only what keeps a user or bucket whole" (:1548-1559): "which
     neither release's JSON carries" becomes v19.2.7's form, with `obj_lock` in the document
     ([rgw_common.cc:2542-2544](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2542-L2544)). F8 and F9 then edit the same bullet for
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
       [rgw_auth_s3.cc:765-797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L765-L797). State the attack that requiring a signed
       Content-Type and a signed x-amz-content-sha256 closes, citing the commit and the code.
       Upstream's own reading is that the escalation comes from unsigned `x-amz-*` headers,
       which v19.2.7 still refuses ([rgw_auth_s3.cc:816-817](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L816-L817)); the task
       verifies that reading against the advisory and the commits, and does not assume it.
     - (b) **rgw-go stays safe without the two requirements.** Show from rgw-go's code that
       whatever (a) found the CVE needs still holds. At least: the payload hash the body is held
       to is the one the signature covers, since `authDataV4` takes it once from
       `expectedPayloadHash` and uses it both as the canonical request's HashedPayload and for
       the body check (internal/auth/sigv4.go:423-429, internal/auth/payload.go:56), as radosgw
       does ([rgw_auth_s3.h:638-660](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.h#L638-L660),
       [rgw_auth_s3.h:641-664](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.h#L641-L664), used at
       [rgw_rest_s3.cc:5858](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L5858)); and every other `x-amz-*` header is still
       refused unsigned.
     - (c) **AWS's rule.** Read and quote, from AWS's documentation and not from memory, its
       SigV4 rule on which headers must be signed, with the URL. Both upstream commits name the
       page, `https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-create-signed-request.html`
       ([rgw_auth_s3.cc:789](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L789)), and v19.2.7's comment records that the page
       contradicts itself on Content-Type ([rgw_auth_s3.cc:797-815](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L797-L815)), so the
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
     ([rgw_auth_s3.cc:788-842](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L788-L842)). Unless `rgw_sigv4_insecure`, `host` and every
     `x-amz-*` header the request carries must be signed, except `x-amz-content-sha256`; a
     present Content-Type need not be. Every refusal is still AccessDenied.
  3. **Tentacle differs:** v20.2.4 also refuses an unsigned Content-Type and an unsigned
     x-amz-content-sha256 ([rgw_auth_s3.cc:765-797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L765-L797)). rgw-go accepts them on
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
    [test_rgw_auth_sigv4.cc:219](https://github.com/ceph/ceph/blob/v19.2.7/src/test/rgw/test_rgw_auth_sigv4.cc#L219)), and any other unsigned `x-amz-*`
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
     ([rgw_op.cc:6584](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6584), [rgw_op.cc:7472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7472)), so a retried
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
     ([rgw_rados.cc:7225-7230](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7225-L7230), the bound at
     [rgw_rados.cc:7216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7216); `RGWRados::guard_reshard` at
     [rgw_rados.cc:7929-7933](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7929-L7933)). The retry re-reads the bucket:
     `init_bs` passes the target's bucket info
     ([rgw_rados.h:947-955](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.h#L947-L955)), and that `BucketShard::init`
     overload reads the instance into it
     ([rgw_rados.cc:2804-2830](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L2804-L2830)). **v20.2.4 differs:** its guard
     breaks on any answer but busy-resharding
     ([rgw_rados.cc:7888-7891](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L7888-L7891)).
  2. **rgw-go:** an index call answering ErrNotFound calls `refreshIndexOp`, which reads the
     instance from RADOS (FD-6). When the re-read instance puts the key on another shard object,
     the call is retried on it, within the same ten calls that busy-resharding retries count.
     When it names the same shard object, the ErrNotFound is returned at once: a write that
     meets it answers 404 NoSuchKey, as now.
  3. A bucket removed meanwhile answers 404 NoSuchKey, as `refreshIndexOp` already maps it.
  4. **Who takes it:** the prepare ([rgw_rados.cc:7289-7291](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7289-L7291)), the
     cancel ([rgw_rados.cc:7395-7397](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L7395-L7397)) and the completion
     manager's retry ([rgw_rados.cc:905](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L905)), and phase 2's OLH ops
     ([rgw_rados.cc:8153](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8153),
     [rgw_rados.cc:8207](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L8207)), which V5 puts on `guardReshard`.
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
     each `ENCODE_START(1, 1)` ([rgw_object_lock.h:34-40](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L34-L40),
     [rgw_object_lock.h:127-135](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.h#L127-L135); the same at
     [rgw_object_lock.h:33-39](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L33-L39), [rgw_object_lock.h:123-131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.h#L123-L131)).
  2. **`ObjectLock()`** decodes `ObjLock` when the flags carry BUCKET_OBJ_LOCK_ENABLED: nil is
     RGWObjectLock's default, enabled with no rule; without the flag it is the disabled zero
     value. **`SetObjectLock`** encodes into `ObjLock` and leaves the flags to the caller. An
     `ObjLock` nobody sets keeps its bytes, so a stored rule re-encodes exactly.
  3. **The dump:** with the flag, `obj_lock` follows `sync_policy`
     ([rgw_common.cc:2538-2544](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2538-L2544)), in v19.2.7's form:
     `enabled` only when true, then `rule_exist`, then `rule` when it exists, holding
     `defaultRetention` with `mode` and `days` when positive, else `years`
     ([rgw_object_lock.cc:93-101](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L93-L101),
     [rgw_object_lock.cc:56-58](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L56-L58),
     [rgw_object_lock.cc:26-33](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L26-L33)). An `ObjLock` that
     does not decode fails `MarshalJSON` with its error.
  4. **The decode:** `obj_lock` is read only under the document's flag
     ([rgw_common.cc:2588-2590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590)), by v19.2.7's decoders
     ([rgw_object_lock.cc:65-71](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L65-L71),
     [rgw_object_lock.cc:44-46](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L44-L46),
     [rgw_object_lock.cc:8-12](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_object_lock.cc#L8-L12)): `rule` only when
     `rule_exist`, and a missing member reads as its zero value, as `T()`
     ([ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346)). A present `obj_lock` sets `ObjLock` to its
     encoding, the default included; an absent one leaves it nil, which F9 reads.
  5. **v20.2.4 differs:** its dump and decode carry no `obj_lock`
     ([rgw_common.cc:2577-2604](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2577-L2604),
     [rgw_common.cc:2606-2647](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L2606-L2647)), and its
     RGWObjectLock dump keeps the old form ([rgw_object_lock.cc:77](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_object_lock.cc#L77)).
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
     yields it ([rgw_common.cc:2588-2590](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L2588-L2590)) and its put stores the decoded
     instance whole
     ([rgw_bucket.cc:2866-2887](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L2866-L2887)).
  2. With no `obj_lock`, an existing instance keeps its stored configuration, where v19.2.7
     resets it to RGWObjectLock() ([ceph_json.h:334-346](https://github.com/ceph/ceph/blob/v19.2.7/src/common/ceph_json.h#L334-L346)) (FD-8). A new
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
     ([rgw_rados.cc:3202](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rados.cc#L3202), [rgw_rados.cc:3299](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rados.cc#L3299)).
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
- **Files:** modify internal/auth/sigv4.go (`canonicalHeadersV4`) and its specs; docs/differences.md;
  docs/ceph-upstream-bugs.md (the entry's "rgw-go:" line).
- **Behaviour:**
  1. radosgw's CVE-2026-54330 check refuses an unsigned header only under `HTTP_X_AMZ_`
     ([rgw_auth_s3.cc:819-841](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L819-L841), [rgw_auth_s3.cc:780-796](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L780-L796)).
     `init_meta_info` then renames six more prefixes onto `x-amz-*` names, into `x_meta_map` and
     `crypt_attribute_map` ([rgw_common.cc:413-464](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L464),
     [rgw_common.cc:426-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L426-L477)). So an unsigned `X-Goog-Meta-*` is stored as user
     metadata, and an unsigned `X-Goog-Server-Side-Encryption-*` reaches the SSE attributes.
  2. rgw-go maps the same prefixes, and has the same gap. Unless insecure, it refuses an unsigned header
     under any prefix that init_meta_info renames, exactly as it refuses an unsigned `x-amz-*`, on both
     releases. That closes the class the CVE fixed. The x-amz-content-sha256 exemption (F4) stays
     limited to that one header name.
  3. The check's prefix set is init_meta_info's. A spec pins it to the tables rgw-go already keeps,
     so the three cannot drift.
  4. SigV2 is unchanged: its string to sign already covers `x_meta_map`
     ([rgw_auth_s3.cc:248](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L248)).
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
- **Exclusions and registry:** the registry entry's text and "rgw-go:" line; differences.md's
  versioned-write refusal already says rgw-go answers 501, which F14 makes true for the completion.
- **Depends on:** nothing. **PR:** its own. **Brief:** about 35 lines.

### Task F15: auth, SigV2 refuses a renamed header carrying an x-amz directive

Found by F13's adversarial review (Minor 5), and numbered here before F13 merged (PP-12).

- **Layer:** auth. **Cluster:** no.
- **Files:** modify internal/auth/sigv2.go and its specs; docs/differences.md; the "rgw-go:" line of
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

### Task F16: s3, CreateBucket reads its object lock header from the renamed map

Superseded by F18 (the owner, 2026-10-14). rgw-go no longer reproduces the renamed map, so CreateBucket
keeps reading the `X-Amz-` header alone, as it does today.

### Task F17: auth, a SigV2 presigned request means the same wherever its header/query split falls

Found by F15's work (its report's concern 5), and numbered here before F15 merged (PP-12).

- **Layer:** auth, op. **Cluster:** no.
- **Files:** modify internal/auth/sigv2.go and its specs, and the read sites the task finds;
  docs/ceph-upstream-bugs.md (a new entry); docs/differences.md.
- **Behaviour:**
  1. On the presigned route, SigV2's string to sign is the canonical header map followed by the
     canonical query map with nothing between them
     ([rgw_auth_s3.cc:164-165](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L164-L165),
     [rgw_auth_s3.cc:165-166](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L165-L166)), and the query map holds only
     `x-amz-meta-*` and `x-amz-security-token`
     ([rgw_auth_s3.cc:175-187](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L175-L187)). Each map is sorted on its own, so
     a run of those names at the end of the headers can move to the front of the query, or back,
     with the string unchanged. radosgw then gives the two forms different meanings: only
     PutObject, PostObject and CompleteMultipartUpload map a query's metadata into the attributes
     (`map_qs_metadata`, [rgw_rest_s3.cc:2619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2619),
     [rgw_rest_s3.cc:2921](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2921),
     [rgw_rest_s3.cc:4101](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4101)), and `sts:authentication` reads the session
     token from the header alone ([rgw_op.cc:904-909](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L904-L909)). rgw-go builds
     the same string (F15's report).
  2. The registry gains the entry, with evidence at both tags, unreproduced (PP-11). It went to
     the rgw-bug-reproduction session on 2026-10-14 as F15-2.
  3. Decision: rgw-go makes the meaning independent of the split rather than refusing either side,
     because a legitimate signer puts these names on both: a presigned PUT's signed `x-amz-meta-*`
     headers, and an STS client's token in the query. On the SigV2 presigned route every read of an
     `x-amz-meta-*` name or the session token sees the header and query forms as one set, and a name
     on both sides is refused with AccessDenied. Each difference this makes from radosgw goes into
     docs/differences.md.
- **Specs:** the header, query and split forms of the same metadata store the same attributes on
  PutObject and CopyObject; the token in the query or the header gives the same
  `sts:authentication`; a name on both sides refused; SigV4 and the SigV2 header route unchanged.
- **s3-tests:** a presigned SigV2 CopyObject whose signed metadata header is moved into the query
  stores that metadata, if s3-tests can presign SigV2 that way; otherwise the report says why not.
- **Depends on:** F15. **PR:** its own. **Brief:** about 40 lines.

### Task F18: auth, authz and s3, the S3 API reads X-Amz- headers only

Decided by the owner on 2026-10-14, in place of F16: rgw-go does not reproduce radosgw's renaming of
six other header prefixes onto `x-amz-`.

- **Layer:** auth, authz, s3. **Cluster:** for the s3-tests run only.
- **Files:** internal/auth (request.go, sigv2.go, sigv4.go, the verifier, their specs, the v2
  vectors, rawdirectives_test.go), internal/authz/env.go and internal/s3/requestattrs.go, with their
  specs; docs/differences.md; the "rgw-go:" lines of docs/ceph-upstream-bugs.md; F13's s3-test on
  jhoblitt/ceph's `rgw-go-s3tests`.
- **Behaviour:**
  1. radosgw's `init_meta_info` files every header under `X-Amz-`, `X-Goog-`, `X-Dho-`, `X-Rgw-`,
     `X-Object-`, `X-Container-` and `X-Account-` into `x_meta_map` under its `x-amz-` name
     ([rgw_common.cc:413-464](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L413-L464),
     [rgw_common.cc:426-477](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L426-L477)). SigV2's string to sign, the stored
     metadata and attrs, and the encryption settings read that map, while most directives read the
     raw header. F13 and F15 closed the gaps this opens, by requiring signatures and by refusing
     relabels.
  2. Decision (the owner, 2026-10-14): rgw-go does not rename. On the S3 API a header under the six
     other prefixes is an ordinary header, as on AWS. The prefixes serve GCS interop, DreamHost and
     Swift's namespace, none of which rgw-go's S3 API offers, and the renaming is what makes a signed
     directive movable.
  3. rgw-go reads, signs and stores `X-Amz-` names alone:
     - SigV2's amz lines name `x-amz-` headers only;
     - SigV4's must-sign rule covers `x-amz-` alone, as radosgw's own check does;
     - stored metadata and attrs come from `X-Amz-` headers only;
     - the authorizer's encryption settings read `X-Amz-` only.

     A header under the six prefixes is never read or stored, signed or not.
  4. Nothing reads those headers any more, so F15's refusal, its refused set and its pin spec go,
     as does F13's six-prefix rule with its `x-rgw-auth` exemption. A relabelled SigV2 request then
     fails its signature with 403 SignatureDoesNotMatch, as on AWS, and an unsigned `X-Goog-Meta-*` is
     ignored.
  5. A Swift API, if one is added later, maps its own namespace (`X-Object-Meta-*`,
     `X-Container-Meta-*`, `X-Account-Meta-*`) explicitly into the same stored attributes. radosgw
     instead files them through the same renaming, with a Swift-specific name normalisation
     ([rgw_common.cc:442-443](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L442-L443)); its Swift handlers read those headers by
     name only to remove metadata ([rgw_rest_swift.cc:748-785](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_swift.cc#L748-L785)).
     This task builds none of it.
- **Specs:** each of the six prefixes:
  - an unsigned header ignored under SigV4 and SigV2;
  - a signed SigV2 relabel of the copy source, the ACL and the tagging failing its signature;
  - `X-Goog-Meta-*` storing no metadata;
  - `X-Amz-` behaviour unchanged, with F13's and F15's `X-Amz-` specs kept.
- **Exclusions,** complete, as the owner asked:
  - one entry, "The S3 API reads X-Amz- headers only", saying what radosgw does with the six
    prefixes, what rgw-go does instead (ignores them: no metadata, attr, signature or directive),
    and the cost: a client that relied on radosgw storing `X-Goog-Meta-*` loses that metadata;
  - it replaces F13's "SigV4 refuses an unsigned header that radosgw renames to `x-amz-*`" and
    F15's "SigV2 refuses a directive under another meta prefix";
  - every other entry that describes the renaming as rgw-go's behaviour changes to match. Today
    that is the SSE 501 entry and "Request headers stored as attrs". The task greps for the rest
    (meta prefix, `x_meta_map`, `X-Goog-`, `init_meta_info`, `X-Rgw-`) and lists each entry it
    changes in its report.
- **Registry:** the "rgw-go:" lines of F13's and F15's entries say rgw-go does not rename.
- **s3-tests:**
  - F15's `test_sigv2_signed_copy_source_moved_to_xgoog` passes unchanged, with 403.
  - F13's `test_sigv4_presigned_put_xgoog_meta_unsigned` asserts a refusal. It is revised to assert
    the property instead, that the presigned PUT stores no metadata from the unsigned header.
  - The next pin bump's known-differences note for that test follows.
- **Depends on:** F17, since both change sigv2.go and the verifier. **PR:** its own. **Brief:**
  about 50 lines.
### Task F19: auth and s3, a header-signed SigV2 request takes no unsigned query metadata or encryption

Found in F17's work (its report's concern 2), and numbered here before F17 merged (PP-12).

- **Layer:** auth, authz, s3. **Cluster:** for the s3-tests run only.
- **Files:** internal/s3/requestattrs.go, internal/authz/env.go (`cryptAttr`'s query read), the auth
  result they read, and their specs; docs/ceph-upstream-bugs.md (a new entry, F17-2);
  docs/differences.md.
- **Behaviour:**
  1. radosgw's PutObject merges the query's `x-amz-meta-*` into `x_meta_map`, and its
     `x-amz-server-side-encryption*` into the encryption settings, overwriting a header's
     ([rgw_rest_s3.cc:2589-2603](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2589-L2603), called at
     [rgw_rest_s3.cc:2619](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2619);
     [rgw_rest_s3.cc:2742](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2742) and
     [rgw_rest_s3.cc:2772](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2772)). CompleteMultipartUpload and PostObject merge
     too. It merges on every route, but SigV2's header route does not sign the query: the query
     map joins the string to sign only on the presigned route
     ([rgw_auth_s3.cc:215-216](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L215-L216),
     [rgw_auth_s3.cc:217-218](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L217-L218)). So a header-signed SigV2 request,
     replayed within its skew window, can gain metadata, or have its signed encryption replaced,
     through query parameters nothing signs. SigV4 signs the canonical query, and the SigV2 presigned
     route signs these names (F17).
  2. rgw-go stores the metadata the same way (F17's report), and `cryptAttr` reads query encryption
     settings. On a header-signed SigV2 request rgw-go reads neither: query `x-amz-meta-*` are not
     stored, and query encryption settings are not read. That matches F18's rule, that rgw-go reads
     nothing a request's signature does not cover. SigV4, the SigV2 presigned route and anonymous
     requests are unchanged, since an anonymous request signs nothing and may send the same names as
     headers.
  3. Each difference from radosgw goes into docs/differences.md. The registry gains the F17-2 entry,
     with evidence at both tags, and the Upstream line in the registry's "pending:
     rgw-bug-reproduction ..." form; it went there on 2026-10-14.
- **Specs:**
  - a header-signed SigV2 PutObject and CompleteMultipartUpload with a query `x-amz-meta-x` stores
    no `x`;
  - one with query encryption settings beside signed header ones uses the header's;
  - presigned SigV2, SigV4 and anonymous requests unchanged.
- **s3-tests:** a header-signed SigV2 PutObject resent with `?x-amz-meta-x=1` stores no `x`, if
  s3-tests can sign SigV2 that way (F15's test shows how).
- **Depends on:** F18, since both change requestattrs.go and `cryptAttr`. **PR:** its own.
  **Brief:** about 35 lines.
### Task F20: s3, PutObject parses partNumber as radosgw does

Found in F17's work (its report's second round), and numbered here before F17 merged (PP-12).

- **Layer:** s3. **Cluster:** for the s3-tests run only.
- **Files:** modify internal/s3/putobject.go and its specs; docs/differences.md, if any difference
  stays.
- **Behaviour:**
  1. radosgw's PutObject reads `partNumber` whenever the query carries one. It answers EINVAL to one
     that `strict_strtol` cannot parse, and to an `uploadId` without a `partNumber`
     ([rgw_rest_s3.cc:2682-2694](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L2682-L2694),
     [rgw_rest_s3.cc:2835-2847](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L2835-L2847);
     [strtol.cc:42-73](https://github.com/ceph/ceph/blob/v19.2.7/src/common/strtol.cc#L42-L73)).
  2. rgw-go reads `partNumber` only beside an `uploadId` (internal/s3/putobject.go). So a PutObject
     carrying an unparseable `partNumber` and no `uploadId` stores an object where radosgw answers
     400. That gap is what let F17's packed sub-resource turn a presigned UploadPart into a
     PutObject in rgw-go, before F17 refused the packing.
  3. rgw-go parses `partNumber` as `strict_strtol` does, on both releases, whenever the query
     carries one. It answers radosgw's status and error code for one that does not parse, and for an
     `uploadId` without one. A `partNumber` that parses but has no `uploadId` behaves as radosgw's
     does; the task reads what that is at both tags.
- **Specs:** `partNumber` of `x`, `1x`, an empty value, a sign, leading spaces and an overflow; with
  and without an `uploadId`; each answered as radosgw answers.
- **s3-tests:** a PutObject with `?partNumber=x` answers 400, unless s3-tests already has one.
- **Depends on:** nothing. **PR:** its own. **Brief:** about 25 lines.

### Task F21: auth, the signature and dispatch read one query

Found by the authentication sweep of 2026-11-21 (F1), on main, and numbered here (PP-12).

- **Layer:** the router, auth, s3 and admin. **Cluster:** for the s3-tests run only.
- **Files:** the request gate (`router.ServeHTTP`, `internal/cli/serve.go`) and its check beside
  `s3.MalformedPath` (`internal/s3/request.go`); SigV4's canonical query (`internal/auth/sigv4.go`)
  and the three argument parsers (`internal/auth/request.go`, `internal/s3/request.go`,
  `internal/admin/request.go`), for the property spec; `docs/differences.md`; the rgw-go line of
  the registry entry the registry sweep adds.
- **Security boundary:** authentication, what a signature covers of the request that is served;
  adversarial review applies (PP-14).
- **Why,** mechanism only:
  - SigV4's canonical query splits each `&` token at its first raw `=` and url-decodes the name
    and the value apart (`canonicalQueryV4`, `internal/auth/sigv4.go:275-304`; `aws4Recode`,
    `internal/auth/encoding.go:66-68`).
  - The argument parser url-decodes the token whole and then splits it. There are three copies,
    one for each reader of the arguments: the verifier's (`parseQuery`,
    `internal/auth/request.go:74-97`), the S3 handler's (`argPairs`,
    `internal/s3/request.go:456-471`) and the admin handler's (`ParseArgs`,
    `internal/admin/request.go:44-64`).
  - `rgwtext.URLDecode` ends its result at a `%` with fewer than two bytes after it and empties
    it at a bad hex digit (`internal/rgwtext/urldecode.go:14-44`). So for a token holding a
    malformed percent-escape the two readers decode different text, and the signature covers a
    query parameter that the argument parser drops.
  - radosgw has the same two readers at both tags: `get_v4_canonical_qs`
    ([rgw_auth_s3.cc:604-660](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L604-L660),
    [rgw_auth_s3.cc:610-666](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L610-L666)), and `RGWHTTPArgs::parse`
    ([rgw_common.cc:848-898](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L848-L898),
    [rgw_common.cc:861-911](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L861-L911)) over `url_decode`
    ([rgw_common.cc:1701-1734](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1701-L1734),
    [rgw_common.cc:1764-1797](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1764-L1797)).
  - The request gate refuses a malformed escape in the path and in the Host's bucket before
    either handler (`internal/cli/serve.go:774-778`; `s3.MalformedPath`,
    `internal/s3/request.go:167-170`; `docs/differences.md`, "Malformed percent-escapes in the
    path"). It does not read the query.
- **Decision:** dispatch and the signature read the same query. A query on which the two readers
  can differ is refused before either reads it, and neither reader is changed. Reason: a
  signature must bind the request that is served, and one refusal ahead of both readers holds
  for every signature version and both handlers. Rejected:
  - building the canonical query from the parsed arguments, which changes the string a
    well-formed request must sign;
  - having the parser split before it decodes, which changes how every argument of every request
    is read: an escaped `=` separates today, as in radosgw.
- **Behaviour:**
  1. The request gate answers net/http's 400 (`badRequest`, `internal/cli/serve.go:802`), before
     either handler and before authentication, to a query holding a token in which a `%` is not
     followed by two hex digits. That is the test `s3.MalformedPath` makes of the path, made of
     each `&`-separated token. It holds on both releases, for every signature version and route,
     and for an anonymous request.
  2. The invariant, which the task proves and does not assume: among the queries the gate
     admits, two with one SigV4 canonical query have the same arguments, name for name and value
     for value, in each of the three parsers. A property spec generates queries against it. Any
     other form it finds on which the readers part is refused by the gate too, and the
     differences entry lists it.
  3. Every query the gate admits is parsed and signed as today.
- **Seams:** the check is a function of the raw query alone, exported beside `s3.MalformedPath`,
  so that the router calls both and a handler mounted without the router can call it. The raw
  query is `vhost.RequestTarget`'s, the one all three parsers and the canonical query read.
- **Specs:** on both releases:
  - a token with a `%` and nothing after it, with one hex digit after it, and with a byte that
    is no hex digit, in a name and in a value: 400 from the gate, with nothing authenticated or
    dispatched, on a SigV4 header-signed request, a SigV4 presigned one, a SigV2 one of each
    route and an anonymous one, through the router to the S3 handler and to the admin handler;
  - a well-formed escape, an escaped `=`, an escaped `&`, a `+`, an empty token and a name with
    no `=`: served as today;
  - the property spec of Behaviour 2, through `canonicalQueryV4` and each parser;
  - the path's refusal, unchanged.
- **s3-tests (PP-19):** a request whose query holds a malformed escape answers 400, signed and
  anonymous. radosgw's parser does not refuse such a query, by reading, so the test is
  `fails_on_rgw`. The task moves the pin with the known-differences lines.
- **Exclusions and registry:** `docs/differences.md`, beside "Malformed percent-escapes in the
  path": the query's refusal, and each further form Behaviour 2 finds. The rgw-go line of the
  registry entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 45 lines.

### Task F22: auth and s3, a SigV2 request's op is one its signature names

Found by the authentication sweep of 2026-11-21 (F2), on main, and numbered here (PP-12). The
defect is public upstream: tracker [#23182](https://tracker.ceph.com/issues/23182) and
[#82418](https://tracker.ceph.com/issues/82418).

- **Layer:** auth, op and s3. **Cluster:** for the s3-tests run only.
- **Files:** the verifier's result (`internal/auth/verifier.go`, `internal/auth/sigv2.go`);
  `op.AuthResult` and `op.AuthRoute` (`internal/op/identity.go`); `s3.ApplyAuth` and the handler
  (`internal/s3/handler.go`); the route (`internal/s3/dispatch.go`); their specs;
  `docs/differences.md`; the rgw-go lines of the registry entries.
- **Security boundary:** authentication; adversarial review applies (PP-14).
- **Why:**
  - SigV2's string to sign names the query only through the canonical resource, which writes
    the sub-resources that are both in `signedSubresourcesV2` and filed by the verifier's parser
    (`internal/auth/sigv2.go:28-34`, `:370-395`; `internal/auth/request.go:24-40`, `:89-95`),
    and, on the presigned route, through the query's `x-amz-meta-*` and `x-amz-security-token`
    (`qsMetaV2`, `internal/auth/sigv2.go:217-229`).
  - S3 dispatch chooses the op by which query keys are present (`method.walk`,
    `internal/s3/dispatch.go:96-105`, over `table`, `:150-306`). The canonical resource never
    writes many of them: the table's rows outside the signed list, `legal-hold`, `retention`,
    `attributes`, `layout`, `replication` and `ownershipControls` among them, the service's
    `usage`, and `encryption` and `object-lock`, which are in the signed list and never filed
    (the registry's "radosgw's SigV2 resource lists encryption and object-lock but never signs
    them").
  - So a SigV2 signature is the same with such a key and without it, while the key chooses the
    op.
  - radosgw is the same at both tags: `op_put` picks `RGWPutObjRetention` and
    `RGWPutObjLegalHold` by a key's presence
    ([rgw_rest_s3.cc:4863-4879](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4863-L4879),
    [rgw_rest_s3.cc:5390-5406](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5390-L5406)), and `signed_subresources` names neither
    key ([rgw_auth_s3.cc:31-60](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L31-L60),
    [rgw_auth_s3.cc:32-61](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L32-L61); `get_canon_resource`,
    [rgw_auth_s3.cc:91-124](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L91-L124), [rgw_auth_s3.cc:92-125](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L92-L125)).
    Tracker #82418 reports the `object-lock` half, and #23182 the admin API's (F23).
  - rgw-go already reads nothing else that a SigV2 signature leaves out: the query's metadata and
    encryption settings (F19; `op.AuthRoute.ReadsQueryMeta` and `ReadsQueryEncryption`,
    `internal/op/identity.go:203-224`) and a system request's `rgwx-` parameters
    (`internal/auth/verifier.go:132-137`).
  - The constraint FD-10 weighs: the Ceph dashboard signs SigV2 and requests `?encryption`,
    `?object-lock` and `?replication` under it
    ([rgw_client.py:725-748](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/services/rgw_client.py#L725-L748),
    [rgw_client.py:882-919](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/services/rgw_client.py#L882-L919),
    [rgw_client.py:1126-1158](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/services/rgw_client.py#L1126-L1158);
    [rgw_client.py:674-697](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/services/rgw_client.py#L674-L697),
    [rgw_client.py:868-905](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/services/rgw_client.py#L868-L905),
    [rgw_client.py:1143-1188](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/services/rgw_client.py#L1143-L1188)), keys its signer
    does not sign either ([awsauth.py:49-56](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/awsauth.py#L49-L56),
    [awsauth.py:49-56](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/awsauth.py#L49-L56)). Those requests verify today because
    neither side signs the key.
- **Decision:** a SigV2 request is not served as an op its signature does not name. The
  canonical resource stays radosgw's, so which signatures verify is unchanged, and the request
  is refused once its signature has verified. The presigned route is refused outright: a
  presigned URL is made to be handed on, so its holder is not its signer. The header route
  follows FD-10, the owner's ruling, since every outcome that closes the defect refuses requests
  the Ceph dashboard makes. Rejected:
  - routing as if the key were absent, which serves an op the request does not name;
  - writing the keys into the canonical resource, which changes the string a SigV2 client must
    sign. A client that signs as radosgw computes, the dashboard's among them, would fail its
    signature, so it refuses the same requests as this decision and breaks the floors' string
    besides. It is the fix #82418 asks of radosgw, and rgw-go takes it when a floor does.
- **Behaviour:**
  1. On the presigned SigV2 route, a request whose route was selected by a query key the
     canonical resource did not write is refused 403 AccessDenied, as the packed sub-resource
     refusals are (`internal/auth/verifier.go:144-165`), on both releases.
  2. On the header route the same request is, by FD-10: (a) refused the same way; (b) refused
     unless it arrived over TLS at rgw-go's own listener (`op.Request.TLS`,
     `internal/op/request.go:81`), where no third party held it, a proxy's `Forwarded` or
     `X-Forwarded-Proto` not counting, since it says nothing of the hop to rgw-go; (c) served as
     now, the registry's rgw-go lines saying rgw-go reproduces the defect. The task stops for
     the ruling before it implements the header route. Behaviour 1 does not wait for it.
  3. The key is the one that selected the route: the row `method.walk` matched, and a key
     `hasObjectSubresource` tests (`internal/s3/dispatch.go:310-317`). A key the query holds
     that selected nothing refuses nothing, since it chose no op.
  4. `list-type` is not such a key: it picks the listing's form, and both listings are one
     action on one bucket. A spec pins that the two routes make the same permission check, so
     that the exemption cannot outlive it.
  5. SigV4 and anonymous requests are unchanged: SigV4 signs the whole query, and an anonymous
     request signs nothing and may name any key.
- **Seams:** the verifier's result says which of the query's keys its signature covers (every
  key on a SigV4 route, the canonical resource's on a SigV2 one), beside `Route` and `QueryMeta`
  (`internal/op/identity.go:94-129`), and `s3.ApplyAuth` carries it to the request as it carries
  `Route` (`internal/s3/handler.go:541-564`). The route names the query key that selected it.
  The handler refuses after `ApplyAuth`, where it refuses an unverified identity
  (`internal/s3/handler.go:405`). `op.AuthRoute` gains the predicate F23 reads too.
- **Specs:** per release:
  - a table over every row of the dispatch table: a SigV2 request of each route signed without
    the row's key and sent with it, refused 403 or served as FD-10 rules, and nothing changed in
    the store; the rows whose key the canonical resource writes fail their signature as today;
  - the table is walked from `table` itself, so a row added later is covered or the spec fails;
  - a key that selects nothing beside one that does; `list-type` under each route, with the
    pin of Behaviour 4;
  - under (b), each header-route row over TLS, over plain HTTP, and over plain HTTP from a
    trusted proxy that says https;
  - SigV4 of both routes and anonymous requests, unchanged.
- **s3-tests (PP-19):** a presigned SigV2 PutObject URL sent with an unsigned key that selects
  another op answers 403, where radosgw serves that op, so `fails_on_rgw`; F15's test shows how
  the suite signs SigV2. The header route's test follows the ruling. Each PR moves the pin with
  its known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry beside "A SigV2 request's unsigned
  query meta and encryption settings are not read"; under (a) or (b), `docs/exclusions.md`
  records the dashboard's panes that stop working, and where; rgw-rs is told. The rgw-go lines
  of the registry entries for #82418 and for "radosgw's SigV2 resource lists encryption and
  object-lock but never signs them".
- **Depends on:** nothing; the header route waits for FD-10. **PR split:** the presigned route,
  then the header route. **Brief:** about 60 lines.

### Task F23: auth and admin, SigV2 on the admin API

Found by the authentication sweep of 2026-11-21 (F3), on main, and numbered here (PP-12). The
defect is public upstream: tracker [#23182](https://tracker.ceph.com/issues/23182) reports
exactly this, and [#82418](https://tracker.ceph.com/issues/82418) is its class on the S3 API.

- **Layer:** auth and admin. **Cluster:** no.
- **Files:** the admin handler (`internal/admin/handler.go`); `op.AuthRoute`
  (`internal/op/identity.go`); their specs; `docs/differences.md`; `docs/exclusions.md` under
  outcomes (a) and (b); the rgw-go line of the registry entry.
- **Security boundary:** authentication of the admin API; adversarial review applies (PP-14).
- **Why:**
  - The admin handler authenticates with the S3 verifier (`internal/admin/handler.go:265`),
    which takes SigV2 on both routes.
  - An admin request's op is chosen by its method, its path and an admin sub-resource of its
    query, and every argument it acts on is a query argument (`ParseArgs`,
    `internal/admin/request.go:44-64`; `opName`, `internal/admin/dispatch.go:155-192`).
  - SigV2's string to sign holds the method, Content-MD5, Content-Type, the date, the `x-amz-*`
    headers and the canonical resource (`canonicalStringV2`, `internal/auth/sigv2.go:107-145`).
    Of the admin sub-resources only `policy` is in the signed list (`:28-34`), and no argument
    is. So one signature covers every admin request of its method and path, whatever its
    sub-resource and arguments, for as long as its date passes the skew check
    (`internal/auth/sigv2.go:77-79`; `authGrace`, `internal/auth/sigv4.go:23-26`), and a
    presigned one until its `Expires`.
  - radosgw is the same at both tags: its admin handlers authenticate through
    `RGW_Auth_S3::authorize` ([rgw_rest_s3.h:643-645](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.h#L643-L645),
    [rgw_rest_s3.h:667-669](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L667-L669)), `RGWHandler_User` picks its op by
    `sub_resource_exists` ([rgw_rest_user.cc:1129-1177](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rest_user.cc#L1129-L1177),
    [rgw_rest_user.cc:1139-1187](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_user.cc#L1139-L1187)), and `signed_subresources`
    names `policy` alone of the admin sub-resources
    ([rgw_auth_s3.cc:31-60](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L31-L60), [rgw_auth_s3.cc:32-61](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L32-L61)).
  - Who signs the admin API with SigV2: the Ceph dashboard. Its `S3Auth` sends
    `Authorization: AWS <key>:<signature>` over HMAC-SHA1
    ([awsauth.py:43-82](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/awsauth.py#L43-L82),
    [awsauth.py:43-82](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/awsauth.py#L43-L82)), and its `RgwClient` calls the admin
    paths with it ([rgw_client.py:459](https://github.com/ceph/ceph/blob/v19.2.7/src/pybind/mgr/dashboard/services/rgw_client.py#L459),
    [rgw_client.py:404](https://github.com/ceph/ceph/blob/v20.2.4/src/pybind/mgr/dashboard/services/rgw_client.py#L404)). go-ceph's admin client,
    which Rook uses, signs SigV4 (`rgw/admin/radosgw.go:88-95` in the fork go.mod pins), so it
    is not affected.
- **Decision:** FD-10, the owner's ruling. SigV2 cannot cover an admin request's arguments, so
  the choice is between refusing it, refusing it where a third party can hold the request, and
  keeping the defect. The spec keeps "every admin endpoint the Rook operator and the Ceph
  dashboard call" (`docs/exclusions.md:509-510`), and outcomes (a) and (b) give part of that up,
  so the task does not decide it.
- **Behaviour:**
  1. A presigned SigV2 admin request is refused 403 AccessDenied once its signature has
     verified, under (a) and (b): its holder is not its signer.
  2. A header-signed SigV2 admin request is, by FD-10: (a) refused the same way; (b) refused
     unless it arrived over TLS at rgw-go's own listener, as F22's Behaviour 2 defines it;
     (c) served as now.
  3. The refusal's message says the admin API takes Signature Version 4 and nothing else. A
     signature that does not verify still answers SignatureDoesNotMatch first, so the refusal
     tells nothing to a requester without the key.
  4. SigV4 admin requests are unchanged.
- **Seams:** the admin handler refuses after `s3.ApplyAuth` (`internal/admin/handler.go:274`),
  beside its refusal of an unverified identity (`:278`), by `r.AuthRoute`
  (`internal/op/request.go:112-114`) and the `op.AuthRoute` predicate F22 adds: whether the
  route's signature covers the whole query.
- **Specs:** per release, for the outcome ruled:
  - a header-signed and a presigned SigV2 request on a route of each admin resource, with and
    without a sub-resource: the ruled answer, and no op run;
  - under (b), each over TLS, over plain HTTP, and over plain HTTP from a trusted proxy that
    says https;
  - a SigV2 request whose signature does not verify: SignatureDoesNotMatch, as today;
  - SigV4 of both routes: unchanged.
- **s3-tests (PP-19):** none: the admin API is not S3's (FD-9's reason). The go-ceph admin
  suite, which signs SigV4, passes unchanged, and an integration spec asserts a SigV2 admin
  request's answer on both releases.
- **Exclusions and registry:** a `docs/differences.md` entry. Under (a) or (b),
  `docs/exclusions.md` says what of the dashboard stops working, and where, in its scope
  sentence and its accepted costs; rgw-rs is told. The registry entry's rgw-go line says what
  rgw-go does, and under (c) that it reproduces the defect.
- **Depends on:** F22, for the predicate, and FD-10. **PR:** one. **Brief:** about 40 lines.

### Task F24: admin, an admin request's op does not depend on the order of its query

Found by the authentication sweep of 2026-11-21 (F4), on main, and numbered here (PP-12).
rgw-bug-reproduction's triage rates it new and low.

- **Layer:** admin. **Cluster:** no.
- **Files:** `internal/admin/request.go` (`Args`), `internal/admin/dispatch.go` (`opName` and
  its helpers), their specs; `docs/differences.md`; the rgw-go line of the registry entry the
  registry sweep adds.
- **Security boundary:** authentication, what a signature covers of an admin request;
  adversarial review applies (PP-14).
- **Why,** mechanism only:
  - The admin op is chosen by the first admin sub-resource name in the query's order
    (`ParseArgs`, `internal/admin/request.go:59-61`; `opName` and `pick`,
    `internal/admin/dispatch.go:155-284`), as radosgw files only the first
    ([rgw_common.cc:962-975](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L962-L975), [rgw_common.cc:975-988](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L975-L988))
    and tests it ([rgw_rest_user.cc:1129-1177](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_rest_user.cc#L1129-L1177),
    [rgw_rest_user.cc:1139-1187](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_rest_user.cc#L1139-L1187)).
  - SigV4's canonical query is sorted by name (`internal/auth/sigv4.go:294`;
    [rgw_auth_s3.cc:620-641](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L620-L641), [rgw_auth_s3.cc:626-647](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L626-L647)),
    so one signature covers every order of one set of parameters.
  - Several of the nine names are also ordinary arguments of other ops. The Admin Ops API gives
    the key ops a `subuser` parameter ([adminops.rst:1118-1137](https://github.com/ceph/ceph/blob/v19.2.7/doc/radosgw/adminops.rst#L1118-L1137),
    [adminops.rst:1242-1268](https://github.com/ceph/ceph/blob/v19.2.7/doc/radosgw/adminops.rst#L1242-L1268);
    [adminops.rst:1839-1858](https://github.com/ceph/ceph/blob/v20.2.4/doc/radosgw/adminops.rst#L1839-L1858),
    [adminops.rst:1963-1989](https://github.com/ceph/ceph/blob/v20.2.4/doc/radosgw/adminops.rst#L1963-L1989)), so a documented request names two of
    them. For such a query the order, which nothing signs, chooses the op.
- **Decision:** the op is chosen from the set of names a query holds, by the request's resource
  and method, and never by their order. Reason: a set is what the signature covers. Rejected:
  refusing every query that names two of the nine, which refuses the documented key ops that
  name a subuser.
- **Behaviour:**
  1. For a resource and method, the selecting names are the ones its helper tests (`userOp`,
     `bucketOp`, `accountOp`, `realmOp`, `internal/admin/dispatch.go:194-277`). A query holding
     none of them is the default op; one holding exactly one is that op, wherever the name
     stands.
  2. A query holding two of a method's selecting names:
     - `key` beside `subuser` on a user PUT or DELETE is the key op, in either order, as the
       Admin Ops API documents the request;
     - every other pair is refused 400 InvalidArgument at dispatch, so before authentication,
       as an unknown method is refused there.
  3. A name that selects nothing for the method no longer hides one that does, where radosgw
     files whichever comes first and serves the default op.
  4. `metadataOp` already tests the arguments themselves (`internal/admin/dispatch.go:230-246`)
     and is unchanged.
  5. The verifier's own parser, which files the first admin sub-resource for SigV2's canonical
     resource (`internal/auth/request.go:92-94`), is F25's.
- **Seams:** `admin.Args` answers which admin sub-resource names the query holds, in place of
  `SubResource`'s first (`internal/admin/request.go:189`), and `opName` picks from that set.
- **Specs:** per release:
  - every documented request of the Admin Ops API that the route table serves selects the op it
    selects today;
  - a property over the route table: for every resource, method and subset of the nine names,
    every order of the query selects one op or is refused;
  - `key` with `subuser` on PUT and DELETE in both orders; every other pair of selecting names
    400, with no op run; a non-selecting name ahead of a selecting one;
  - a SigV4 request signed in one order and sent in another: the same op.
- **s3-tests (PP-19):** none: the admin API is not S3's. The go-ceph admin suite passes
  unchanged, and an integration spec asserts one reordered request on both releases.
- **Exclusions and registry:** a `docs/differences.md` entry: the pair's rule, the 400, and
  Behaviour 3. The rgw-go line of the registry entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 40 lines.

### Task F25: auth, SigV2's canonical resource writes `policy` wherever the query names it

Found by the authentication sweep of 2026-11-21 (F5), on main, and numbered here (PP-12).

- **Layer:** auth. **Cluster:** for the s3-tests run only.
- **Files:** the verifier's parser (`internal/auth/request.go`), the canonical resource
  (`internal/auth/sigv2.go`), their specs and the SigV2 vectors; `docs/differences.md`; the
  rgw-go line of the registry entry the registry sweep adds.
- **Security boundary:** authentication; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - `policy` is in `signedSubresourcesV2` (`internal/auth/sigv2.go:28-34`), and the canonical
    resource writes a name of that list only when the verifier's parser filed it
    (`internal/auth/sigv2.go:381-393`).
  - The parser files `policy` only when it is the first of the nine admin sub-resource names
    the query holds (`internal/auth/request.go:37-40`, `:92-94`). The other eight are not in
    the signed list.
  - S3 dispatch tests the argument itself, wherever it stands (`method.walk`,
    `internal/s3/dispatch.go:96-105`; the `policy` rows at `:169`, `:205` and `:227`).
  - So the canonical resource and dispatch read the query differently: a query can hold a
    `policy` that dispatch reads and the signature does not cover.
  - radosgw is the same at both tags: `is_policy_op` tests the argument
    ([rgw_rest_s3.h:704-706](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.h#L704-L706), [rgw_rest_s3.h:731-733](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.h#L731-L733)),
    `RGWHTTPArgs::append` files `policy` as a sub-resource only when no admin sub-resource came
    before it ([rgw_common.cc:962-975](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L962-L975),
    [rgw_common.cc:975-988](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L975-L988)), and `get_canon_resource` reads the filed ones
    ([rgw_auth_s3.cc:91-124](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_auth_s3.cc#L91-L124), [rgw_auth_s3.cc:92-125](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_auth_s3.cc#L92-L125)).
- **Decision:** the canonical resource and dispatch read the same query: the verifier's parser
  files `policy` whenever the query names it, so the canonical resource always writes it and the
  signature compare decides. Reason: `policy` is a name SigV2 does sign, so the fix is to sign
  it everywhere, not to refuse after the compare as F22 must for the names SigV2 never signs.
  No client of the S3 API or of the Admin Ops API sends `policy` behind another of the nine
  names, so the string a documented request signs is unchanged.
- **Behaviour:**
  1. The verifier's parser files `policy`, with an empty value, for every query that holds the
     name, whatever precedes it. A request whose signature was made without `policy` in the
     canonical resource then fails with SignatureDoesNotMatch.
  2. A query whose first admin sub-resource is `policy`, and one that holds none of the nine,
     sign the string they sign today.
  3. Which of the other eight names is filed is unchanged, and none of them is signed.
- **Seams:** none new: `parseQuery`'s filing (`internal/auth/request.go:89-95`).
- **Specs:** on both releases:
  - on both SigV2 routes, `policy` alone, first, and behind each of the other eight names: the
    canonical resource holds `?policy` in every case; a signature made without it fails;
  - the SigV2 vectors for `?policy` first, unchanged;
  - SigV4, unchanged.
- **s3-tests (PP-19):** a SigV2 request whose query holds `policy` behind another of the nine
  names, signed without `policy` in its canonical resource, answers 403 SignatureDoesNotMatch.
  radosgw's answer differs, so the test is `fails_on_rgw`. The task moves the pin with the
  known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go line of the registry
  entry.
- **Depends on:** F22, which changes the same verifier path. **PR:** one. **Brief:** about 30
  lines.

### Task F26: op and s3, a Content-MD5 binds the body of a parameter document

Found by the authentication sweep of 2026-11-21 (F6), on main, and numbered here (PP-12).
Compare tracker [#81230](https://tracker.ceph.com/issues/81230), which reports the payload hash
of the same bodies.

- **Layer:** op, s3 and admin. **Cluster:** for the s3-tests run only.
- **Files:** the two whole-body readers, `op.ReadParamBody` (`internal/op/parambody.go`) and
  `readParamBody` (`internal/s3/bucket.go`); the admin metadata put's reader
  (`internal/admin/metadata.go`); PutBucketLifecycle's own check (`internal/op/lifecycle.go`);
  their specs; `docs/differences.md`; the rgw-go line of the registry entry.
- **Security boundary:** authentication, what a signature covers of the body; adversarial
  review applies (PP-14).
- **Why:**
  - A SigV2 string to sign names the body only through `Content-MD5`
    (`internal/auth/sigv2.go:108-112`, `:137`), and a SigV4 request over `UNSIGNED-PAYLOAD`
    names it only through a signed `content-md5` header.
  - Three ops hold the body to that digest: PutObject (`internal/s3/putobject.go:126-132`),
    UploadPart (`internal/s3/multipart.go:308-313`) and PutBucketLifecycle
    (`internal/op/lifecycle.go:161-172`).
  - Every other op that reads a parameter document reads it through `readParamBody`
    (`internal/s3/bucket.go:149-174`) or `op.ReadParamBody` (`internal/op/parambody.go:34-56`),
    and neither reads the header: the bucket policy, ACLs, tagging, CORS, versioning, website,
    encryption, public access block, object lock, retention, legal hold, DeleteObjects,
    CompleteMultipartUpload, CreateBucket, and the admin API's quota documents. So such an op
    acts on whatever body arrives under the signed digest.
  - rgw-go already holds such a body to a signed `x-amz-content-sha256` (`docs/differences.md`,
    "A body read whole is checked against its signed hash"), where radosgw drops that verdict
    (tracker #81230). The Content-MD5 is the binding left open.
  - radosgw reads `HTTP_CONTENT_MD5` in three places alone at both tags, PutObject's
    `get_params`, the POST object op and `RGWPutLC::execute`
    ([rgw_rest.cc:1068](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1068), [rgw_rest.cc:1426](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1426),
    [rgw_op.cc:5945-5984](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5945-L5984); [rgw_rest.cc:1068](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1068),
    [rgw_rest.cc:1426](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1426), [rgw_op.cc:6611-6650](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6611-L6650)).
  - Amazon S3 uses the header "to ensure that your request body has not been altered in
    transit", and requires it of DeleteObjects
    ([service-2.json:374](https://github.com/boto/botocore/blob/4c688659d4a2ccc0edcdc3357107a534a52e478d/botocore/data/s3/2006-03-01/service-2.json#L374)).
- **Decision:** the two readers make the comparison, not each op. Reason: one seam covers every
  op that reads a parameter document, one added later included, and it is where the signed
  payload hash is already enforced.
- **Behaviour:**
  1. When the request carries `Content-MD5`, a whole-body reader answers 400 InvalidDigest for
     one that does not decode to 16 bytes, decoded as PutObject decodes it
     (`internal/s3/putobject.go:126-132`), and 400 BadDigest for a body whose MD5 differs, before
     the document is parsed and before anything is written.
  2. The body is read to its end first, so the payload's own verdict, the signed hash's among
     them, is answered ahead of the digest's, as PutBucketLifecycle orders them today.
  3. PutBucketLifecycle keeps its answers and messages; it makes one comparison, not two.
  4. The admin metadata put's body (`readMetadataBody`, `internal/admin/metadata.go:206-227`) is
     held the same way.
  5. A request without the header is unchanged. The header is never required, where Amazon S3
     requires it of DeleteObjects; `docs/differences.md` says so.
- **Seams:** `op.ReadParamBody` and `readParamBody` read `Content-MD5` from the request they are
  given; no caller changes.
- **Specs:** per release:
  - a table over every route whose op reads a parameter document: a digest of another body 400
    BadDigest with nothing stored, one that does not decode 400 InvalidDigest, the body's own
    digest served, none served;
  - the table is checked against the callers of the two readers, so an op added later is
    covered or the spec fails;
  - a body whose signed `x-amz-content-sha256` and `Content-MD5` both fail: the payload's
    answer;
  - PutObject, UploadPart and PutBucketLifecycle, unchanged.
- **s3-tests (PP-19):** PutBucketPolicy, PutBucketTagging and DeleteObjects sent with the
  `Content-MD5` of another body answer 400 BadDigest, where radosgw serves them, so
  `fails_on_rgw`. The task moves the pin with the known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry beside "A body read whole is
  checked against its signed hash". The rgw-go line of the registry entry, which today is an
  aside of "radosgw ignores a payload-hash mismatch on bodies read by read_all_input".
- **Depends on:** nothing. **PR:** one. **Brief:** about 40 lines.

### Task F27: formatter and admin, a tag's key is never written as markup

Found by the multipart and admin sweep of 2026-11-21 (F2), on main, and numbered here (PP-12).

- **Layer:** formatter and admin. **Cluster:** no.
- **Files:** `internal/formatter/json.go`, `internal/formatter/xml.go`, `internal/admin/bucket.go`
  (`dumpBucketStats`), their specs; `docs/differences.md`; the rgw-go line of the registry entry
  the registry sweep adds.
- **Security boundary:** the admin API's response, read by an administrator's tools, carries
  bytes a bucket's tagger chose; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - `GET /admin/bucket?bucket=<b>` renders the bucket's tag set with each tag's key as a
    member's name (`dumpBucketStats`, `internal/admin/bucket.go:175-184`).
  - The JSON formatter writes a name raw between two quotes (`printName`,
    `internal/formatter/json.go:55-75`), and the XML formatter writes it raw as the element's
    name (`element`, `internal/formatter/xml.go:98-103`; `elementName`, `:42-53`).
  - A tag key is any bytes up to 128 (`Set.Add`, `internal/tags/tags.go:58-71`), and
    PutBucketTagging checks nothing more (`internal/s3/bucketsubres.go:105-117`). So a key's
    bytes are written into the document's structure, not into a string.
  - radosgw is the same at both tags: `RGWObjTags::dump` passes the key as the name
    ([rgw_tag.cc:59-66](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.cc#L59-L66), [rgw_tag.cc:59-66](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_tag.cc#L59-L66)), bucket
    stats calls it ([rgw_bucket.cc:1419-1425](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_bucket.cc#L1419-L1425),
    [rgw_bucket.cc:1584-1590](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_bucket.cc#L1584-L1590)), and `JSONFormatter::print_name`
    writes the name unescaped ([Formatter.cc:199-218](https://github.com/ceph/ceph/blob/v19.2.7/src/common/Formatter.cc#L199-L218),
    [Formatter.cc:207-226](https://github.com/ceph/ceph/blob/v20.2.4/src/common/Formatter.cc#L207-L226)).
- **Decision:** the formatters escape a name; the admin route is not special-cased. Reason:
  every caller that passes a computed name is covered, one added later included, and a name
  that needs no escaping keeps radosgw's bytes, so the documents radosgw's own tags produce do
  not change.
- **Behaviour:**
  1. The JSON formatter writes a member's name as a JSON string, escaped as it escapes a string
     value.
  2. The XML formatter writes as an element's name only a name that is an XML name. For any
     other, the name's bytes are written as escaped text and never as markup, in a shape the
     task chooses and `docs/differences.md` records; two distinct keys must stay distinct in it.
  3. The tag set's shape, a `tagset` object with a member for each tag, is unchanged for every
     key that needs no escaping.
- **Seams:** `printName` and `elementName`; no caller changes.
- **Specs:** per release, in JSON and in XML:
  - bucket stats of a bucket whose tag keys hold a quote, a backslash, a control byte, `<`,
    `>`, `&`, a space and a NUL: the document parses, holds each key once as a key, and holds
    no member or element the tag set did not name;
  - keys that need no escaping: radosgw's bytes;
  - every other formatter call with a computed name, listed by the task: unchanged bytes.
- **s3-tests (PP-19):** none: the admin API is not S3's. A go-ceph admin suite case, or an
  integration spec on both releases, reads the stats of a bucket with such a key.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go line of the registry
  entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 30 lines.

### Task F28: driver, a bucket instance put bounds a new instance's shard count

Found by the multipart and admin sweep of 2026-11-21 (F4), by reading and not run, and numbered
here (PP-12).

- **Layer:** driver. **Cluster:** no.
- **Files:** `internal/driver/metadata_instance.go` (`newInstance`) and its specs;
  `docs/differences.md`; the rgw-go line of the registry entry the registry sweep adds.
- **Security boundary:** none: the put needs the `metadata=write` cap, so the standard review
  applies. The check keeps a careless or hostile document from ending the process.
- **Why:**
  - `BucketInfo.UnmarshalJSON` reads `num_shards` as any 32-bit count
    (`internal/meta/bucket_json.go:131`).
  - `newInstance` checks a new instance's marker, placement and id, not its shard count
    (`internal/driver/metadata_instance.go:374-394`). The put then writes the instance (`:277`)
    and only then builds the index (`:291`), whose first step allocates a name for every shard
    (`shardOIDs`, `internal/driver/index.go:57-67`). An allocation the host cannot back ends the
    process, and the stored instance makes every later walk of its shards allocate the same.
  - An existing instance cannot change its shard count (`keepInstance`,
    `internal/driver/metadata_instance.go:345-370`), so only a new instance is exposed.
  - radosgw has the same shape at both tags: `init_index` builds a map of every shard before it
    writes one ([svc_bi_rados.cc:366-367](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_bi_rados.cc#L366-L367),
    [svc_bi_rados.cc:470-471](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_bi_rados.cc#L470-L471)). Whether radosgw bounds the count
    elsewhere on this path was not checked.
- **Decision:** a new instance whose shard count is above `meta.ShardsMax`, 65521
  (`internal/meta/bucket_layout.go:430-434`), is refused before anything is written. Reason: no
  key hashes to a shard past it, so such a count is never a working index, and it is the bound
  rgw-go already gives `rgw_gc_max_objs`.
- **Behaviour:**
  1. `newInstance` answers 400 InvalidArgument for a document whose `num_shards` exceeds
     `meta.ShardsMax`, with no instance object, claim or index object written.
  2. A count at or below the bound, 0 included, is taken as today.
  3. An instance a radosgw stored with a larger count is read as today; the task says in
     `docs/differences.md` what rgw-go does on meeting one.
- **Seams:** none new.
- **Specs:** per release, on fakerados:
  - a put of a new instance with `num_shards` of 65521, 65522 and 4294967295: the first stored,
    the others 400 with nothing written;
  - the first spec the sweep could not run: a count of 2000000 refused without the allocation;
  - a put over an existing instance, unchanged.
- **s3-tests (PP-19):** none: the metadata API is not S3 (FD-9).
- **Exclusions and registry:** a `docs/differences.md` entry beside "Counts too large for
  radosgw's integers"; the rgw-go line of the registry entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 25 lines.

### Task F29: op and admin, the usage listing renders as it reads

Found by the multipart and admin sweep of 2026-11-21 (F5), by reading and not run, and numbered
here (PP-12).

- **Layer:** op and admin. **Cluster:** no.
- **Files:** `internal/op/adminusage.go` (`ShowUsage`), `internal/admin/usage.go` (`getUsage`,
  `dumpUsage`), their specs; `docs/memory.md`.
- **Security boundary:** none: the route needs the `usage=read` cap, so the standard review
  applies.
- **Why:**
  - `ShowUsage.Execute` appends every record of every page to its result and the route renders
    only after the last page (`internal/op/adminusage.go:105-147`;
    `internal/admin/usage.go:46-64`), so a request over the whole usage log holds all of it in
    memory, outside any budget.
  - radosgw flushes its formatter after each bucket record and keeps only the per-user summary
    ([rgw_usage.cc:119](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_usage.cc#L119), [rgw_usage.cc:119](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_usage.cc#L119)).
- **Decision:** the entries are rendered page by page, as radosgw renders them, and the op holds
  the summary alone. Reason: the memory a request takes must not grow with the log. Rejected:
  recording the bound, which leaves an operator's whole-log request able to exhaust the gateway.
- **Behaviour:**
  1. With `show-entries`, each page's records are written to the response as the page is read,
     in the document radosgw writes, byte for byte.
  2. The summary is written after the last page, as today.
  3. An error after the first page has been written ends the response there and is logged, as
     the admin handler logs a route that fails after its response started
     (`internal/admin/handler.go:288-299`), where radosgw's stream ends the same way. An error
     before any page is written answers as today.
  4. A client that goes away ends the listing, as today (`internal/op/adminusage.go:113-118`).
- **Seams:** `ShowUsage` hands each page to a sink the route gives it, in place of collecting
  `Entries`; the route renders through the formatter it holds.
- **Specs:** per release:
  - a listing over several pages: the document's bytes equal today's, and the op holds no more
    than one page of records at a time, shown by a store that fails the spec if a page is kept;
  - `show-entries=false`, `show-summary=false`, the category filter and a user's or a bucket's
    scope: unchanged bytes;
  - an error on the first page and on a later one.
- **s3-tests (PP-19):** none: the admin API is not S3's. The go-ceph admin suite's usage cases
  pass unchanged.
- **Exclusions and registry:** none: the bytes are radosgw's. `docs/memory.md` drops the route
  from what it lists as unbounded, if it lists it.
- **Depends on:** nothing. **PR:** one. **Brief:** about 25 lines.

### Task F30: frontend, a read no handler makes has a deadline, and `OPTIONS *` reaches the handler

Found by the frontend sweep of 2026-11-28 (F1), reproduced on both releases against the
product's own `frontend.Server`, and numbered here (PP-12).

- **Layer:** frontend. **Cluster:** for the s3-tests run only.
- **Files:** `internal/frontend/server.go`, `internal/frontend/deadline.go`, their specs; the
  call that wraps the router (`internal/cli/serve.go`) and a gateway spec served through
  `frontend.Server`; `docs/differences.md`.
- **Security boundary:** the frontend, the first code an untrusted byte reaches; adversarial
  review applies (PP-14).
- **Why,** mechanism only:
  - The http.Server has no read timeout (`internal/frontend/server.go:228-246`). net/http
    therefore clears the connection's read deadline once a request's header is read
    ([server.go:1036-1040](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1036-L1040), [server.go:1103](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1103)), and every later read is
    timed by `frontend.Deadlines`, which wraps the handler
    (`internal/frontend/deadline.go:37-61`; `internal/cli/serve.go:346`).
  - net/http answers two kinds of request itself, and neither reaches that handler:
    - `OPTIONS *`, which it serves with its own handler unless `DisableGeneralOptionsHandler`
      is set ([server.go:3432-3434](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L3432-L3434)), and rgw-go does not set it. That handler
      answers 200 with `Content-Length: 0`, after it reads up to 4 KiB of a declared body
      ([server.go:4187-4198](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L4187-L4198));
    - a request whose `Expect` is not `100-continue`: net/http writes the 417 and finishes the
      request ([server.go:2133-2136](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L2133-L2136), [server.go:2280-2296](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L2280-L2296)), which
      closes the body ([server.go:1737](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1737)), and the close reads what a declared body
      still owes, up to 256 KiB ([transfer.go:1019-1035](https://github.com/golang/go/blob/go1.27.3/src/net/http/transfer.go#L1019-L1035)).
  - Neither of those reads has a deadline, so `request_timeout_ms` does not bound it.
  - radosgw, by reading and not run: it answers `OPTIONS *` with 405 MethodNotAllowed, since a
    target with no leading slash names no bucket
    ([rgw_rest_s3.cc:4938-4939](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4938-L4939), [rgw_rest_s3.cc:5468-5469](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5468-L5469)),
    the service handler has no OPTIONS op ([rgw_rest.h:549](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L549),
    [rgw_rest.h:561](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L561)) and an op that is null is refused
    ([rgw_process.cc:325-329](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L325-L329), [rgw_process.cc:327-331](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L327-L331)).
    It times every read it makes, the discard of an unread body among them
    ([rgw_asio_frontend.cc:365-388](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L365-L388),
    [rgw_asio_frontend.cc:376-399](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L376-L399)), and reads `Expect` only to note a
    `100-continue` ([rgw_asio_frontend.cc:290](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L290),
    [rgw_asio_frontend.cc:291](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L291)).
- **Decision:** one change for each way a request misses the handler.
  - `OPTIONS *` is passed to the handler, so it is routed, timed and answered as any request.
  - A read that no handler makes is bounded by the server: the http.Server takes
    `request_timeout_ms` as its read timeout, and the handler's wrapper clears that deadline as
    it starts and times each read itself, as today. Reason: net/http has no hook around the
    answers it makes itself, and its read timeout covers the reads it makes outside a handler.
  - Rejected: a deadline armed around every read in a wrapper of the connection. net/http's
    background read, which waits for the client to go away while a handler runs, must have none
    (`deadlineBody.Read`, `internal/frontend/deadline.go:136-141`), and a wrapper of the
    connection cannot tell that read from another.
- **Behaviour:**
  1. `OPTIONS *` reaches the router behind the deadlines. A target with no leading slash names
     no bucket (`splitURI`, `internal/s3/request.go:406-416`) unless the Host names one
     (`hostedURI`, `:180-186`), so it gets the S3 handler's answer to a service OPTIONS, 405
     MethodNotAllowed (`internal/s3/dispatch.go:114-115`), with a request id. With S3 off it
     gets the answer `OPTIONS /` gets.
  2. A request net/http answers itself has `request_timeout_ms`, counted from the start of the
     request, for every read net/http still makes of it. A read past that fails and the
     connection ends. The 417's read of a declared body is the one the sweep found; the bound
     holds for any other.
  3. A request a handler serves is timed as today: each read and each write within
     `request_timeout_ms`, and no bound on the whole. An upload or a download that takes many
     timeouts in all still completes, and so does a request with no body whose handler runs
     longer than the timeout.
  4. `request_timeout_ms=0` sets no deadline anywhere, as today.
  5. The 417 itself stays (`docs/differences.md`, "Expectations other than 100-continue").
- **Seams:** the server's read timeout and the handler's deadlines are set together, by one
  constructor in `internal/frontend`, from the one `request_timeout_ms`. A handler served with
  the first and without the second would have every upload longer than the timeout cut, so
  `frontend.New` (`internal/frontend/server.go:46-57`) takes the bare handler and wraps it, and
  `internal/cli/serve.go:346` passes the router alone.
- **Specs:** through `frontend.Server` on a loopback port, as the sweep ran its own, and the
  gateway ones on both releases. The sweep's failing specs are untracked files in its worktree
  and are not fit to merge as they stand:
  - `OPTIONS *`: 405 MethodNotAllowed with `x-amz-request-id`; with a Host that names a bucket,
    the answer that routing gives, pinned; `GET *`, unchanged;
  - `OPTIONS *` that declares a body and sends none, by a Content-Length, by an unfinished
    chunk and as HTTP/1.0: the connection is ended one timeout after the last byte;
  - a PUT with `Expect: 200` that declares a body and sends none: the 417 at once, and the
    connection ended within one timeout; with its body sent: the 417 and the close, as today;
  - a body sent in pieces, each inside the timeout, over several timeouts in all: stored whole;
    a response read as slowly: delivered whole; a request with no body whose handler outlasts
    the timeout: answered; each on a plain and on a TLS listener;
  - a second request on a kept connection, and the header and idle timeouts: the existing specs
    of `internal/frontend/deadline_test.go` and `internal/frontend/server_test.go`, unchanged;
  - `request_timeout_ms=0`: no deadline.
- **s3-tests (PP-19):** a raw `OPTIONS *` answers 405 MethodNotAllowed. radosgw's answer is read
  from source and not run, so the task's cluster run decides whether the test carries
  `fails_on_rgw`. The stalled reads have no test: each takes `request_timeout_ms` of silence.
- **Exclusions and registry:** `docs/differences.md`: "Timeouts per operation" says a read
  net/http makes outside a handler has one timeout for the whole of it, where beast times each
  read; "Expectations other than 100-continue" says when the 417's connection ends. No registry
  entry: radosgw is not at fault.
- **Depends on:** nothing. **PR:** one. **Brief:** about 35 lines.

### Task F31: frontend, the connection of a request that names two framings is not reused

Found by the frontend sweep of 2026-11-28 (F4), reproduced on both releases against the
product's own `frontend.Server`, and numbered here (PP-12).

- **Layer:** frontend. **Cluster:** for the s3-tests run only.
- **Files:** `internal/frontend/server.go`, a wrapper of the accepted connection beside
  `noDelayOff`, the handler's wrapper (`internal/frontend/deadline.go`), their specs;
  `docs/differences.md`.
- **Security boundary:** the frontend, and where a request ends on its connection; adversarial
  review applies (PP-14).
- **Why,** mechanism only:
  - net/http serves a request that carries both a Content-Length and
    `Transfer-Encoding: chunked`. It deletes the Content-Length, reads the body by its chunks,
    and keeps the connection for the next request ([transfer.go:713-729](https://github.com/golang/go/blob/go1.27.3/src/net/http/transfer.go#L713-L729)). The
    comment there quotes RFC 9112: such a message "ought to be handled as an error".
  - The field is deleted before any handler runs, so no handler can tell that it was sent, and
    rgw-go makes no check of its own.
  - Read from net/http and not run: on an HTTP/1.0 request it deletes the Transfer-Encoding
    instead and frames the request by its Content-Length, or as having no body
    ([transfer.go:644-649](https://github.com/golang/go/blob/go1.27.3/src/net/http/transfer.go#L644-L649)), and a kept HTTP/1.0 connection is read on.
  - radosgw refuses the request whichever field comes first, and ends the connection. beast
    reports a Content-Length after a chunked Transfer-Encoding ([basic_parser.ipp:819-821](https://github.com/boostorg/beast/blob/boost-1.82.0/include/boost/beast/http/impl/basic_parser.ipp#L819-L821),
    [basic_parser.ipp:725-727](https://github.com/boostorg/beast/blob/boost-1.87.0/include/boost/beast/http/impl/basic_parser.ipp#L725-L727)) and a Transfer-Encoding after a Content-Length
    ([basic_parser.ipp:870-875](https://github.com/boostorg/beast/blob/boost-1.82.0/include/boost/beast/http/impl/basic_parser.ipp#L870-L875), [basic_parser.ipp:776-781](https://github.com/boostorg/beast/blob/boost-1.87.0/include/boost/beast/http/impl/basic_parser.ipp#L776-L781)), at the Boost release each tag builds
    ([BuildBoost.cmake:162](https://github.com/ceph/ceph/blob/v19.2.7/cmake/modules/BuildBoost.cmake#L162),
    [BuildBoost.cmake:166](https://github.com/ceph/ceph/blob/v20.2.4/cmake/modules/BuildBoost.cmake#L166)), and radosgw answers a header beast
    cannot parse with 400 and returns ([rgw_asio_frontend.cc:274-288](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L274-L288),
    [rgw_asio_frontend.cc:275-289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L275-L289)). Read from source, not run.
  - So rgw-go goes on reading a connection on which it and another reader of the same bytes
    may have placed the end of a request differently.
- **Decision:** rgw-go does not reuse the connection. The response to such a request carries
  `Connection: close`, and the connection ends after it. The request itself is still served on
  its chunked framing, where radosgw answers 400, and that is recorded. Reason: the end of the
  request is what the two readers may disagree on, and a connection that is closed carries no
  next request. Rejected:
  - answering 400, as radosgw does. The field can be seen only before net/http parses the
    header, so by a second reader, which must follow every request's framing to find the next
    header. `docs/differences.md`, "Expectations other than 100-continue", keeps the 417 for
    that reason: "a second HTTP parser in front of net/http's whose framing could disagree with
    it" (`docs/differences.md:3524-3528`). The same reason holds here;
  - closing after every chunked request. botocore sends an upload whose checksum travels in a
    trailer with `Transfer-Encoding: chunked`
    ([httpchecksum.py:471](https://github.com/boto/botocore/blob/4c688659d4a2ccc0edcdc3357107a534a52e478d/botocore/httpchecksum.py#L471)),
    so each such PutObject and UploadPart would open a connection, and under TLS make a
    handshake;
  - recording the difference alone, which leaves the connection in use.
- **Behaviour:**
  1. A request whose header held a framing field that net/http deleted, a Content-Length beside
     a chunked Transfer-Encoding or an HTTP/1.0 request's Transfer-Encoding, is answered with
     `Connection: close`, and its connection is closed after the response: on a plain and on a
     TLS listener, whatever the order and the case of the fields, and wherever the request
     stands on its connection, a pipelined one included.
  2. The detection reads no framing. **Sketch, not run:** for each connection the frontend
     counts two things per field. One is the lines that begin `Content-Length:` or
     `Transfer-Encoding:`, in any case, in the bytes it hands net/http. The other is the
     requests its handler serves that still report the field: a Content-Length left in the
     header map, not the request's length, which is 0 for a request that sent none, and a
     transfer encoding net/http recorded. A request served while the lines of either field are
     ahead of the requests that reported it is one the rule closes. A body that holds such a
     line, a field sent twice and a trailer of that name each raise the lines alone. So the
     rule may close a connection it need not, and never keeps one it must close.
  3. The task proves 2 before it relies on it, by the property spec below. If the property
     cannot be made to hold without reading framing, the task stops, and the owner rules
     between closing after every chunked request and recording the difference.
  4. Every other request is served, and its connection kept, as today.
  5. The scan reads every byte of every request, so the task measures it on the PutObject
     benchmark and records the cost, and records how often a body of text that holds such lines
     costs its client a connection. A cost the owner would not take is his to rule on.
- **Seams:** the count sits where the frontend sees the bytes net/http reads: a wrapper of each
  accepted connection, above TLS, which passes on what net/http asks of a connection, the TLS
  handshake and state among them. The handler's half sits with the deadlines. F30 changes the
  same server and the same wrapper, so the two merge one after the other.
- **Specs:** through `frontend.Server` on a loopback port, on a plain and on a TLS listener:
  - a PUT with `Content-Length: 3` and `Transfer-Encoding: chunked`, in both orders and in
    mixed case: the response carries `Connection: close`, and a request written behind it on
    the connection gets no answer. Today it is answered 200 and the connection kept;
  - the same request as the second and as the tenth on a kept connection, and pipelined behind
    another in one write;
  - an HTTP/1.0 request with `Connection: keep-alive` and a Transfer-Encoding, with and without
    a Content-Length: closed the same way, which also runs what Why only read;
  - kept open: a chunked PUT alone, a PUT with a Content-Length alone, and fifty of the two in
    turn on one connection;
  - closed and not refused: a request after a body that holds such a line, and after a request
    that sent Content-Length twice;
  - the property: over generated sequences of requests on one connection, delivered in reads
    split at any byte, every request whose header held a framing field net/http deleted has its
    connection closed;
  - the line matcher alone: a stream split at any byte counts the lines the whole stream
    counts, as a fuzz target.
- **s3-tests (PP-19):** a raw request that carries both fields has its connection closed after
  the response. Both gateways close it, radosgw after its 400 by reading, so the test asserts
  the close and not the status, and carries no marker. The task moves the pin.
- **Exclusions and registry:** a `docs/differences.md` entry beside "A chunked request's
  Transfer-Encoding reads as `chunked`": the request is served where radosgw answers 400, its
  connection is closed, and which other connections the rule closes. No registry entry: radosgw
  is not at fault.
- **Depends on:** F30. **PR:** one. **Brief:** about 40 lines.

### Task F32: frontend, what net/http answers or rewrites before a handler is recorded and pinned

Found by the frontend sweep of 2026-11-28 (F2, F3, and its list of differences that
`docs/differences.md` does not record), reproduced on both releases, and numbered here (PP-12).

- **Layer:** docs and specs; no behaviour changes. **Cluster:** no.
- **Files:** `docs/differences.md`; a gateway spec served through `frontend.Server`
  (`internal/cli`), and the write ops' specs for the stored attr (`internal/s3`).
- **Security boundary:** none: nothing on the request path changes, so the standard review
  applies.
- **Why:**
  - The sweep's F2. net/http's request reader adds `Cache-Control: no-cache` to the header map
    of a request that carries `Pragma: no-cache` and no Cache-Control
    ([request.go:1177](https://github.com/golang/go/blob/go1.27.3/src/net/http/request.go#L1177), [response.go:218-224](https://github.com/golang/go/blob/go1.27.3/src/net/http/response.go#L218-L224)). rgw-go builds a write's generic attrs from that map
    (`requestEnv`, `internal/s3/requestattrs.go:135-143`; `requestAttrs`, `:264-271`; the
    table's row, `:60`), so the header becomes the object's `user.rgw.cache_control`, and every
    later GET and HEAD sends it. PutObject was run. CopyObject, CreateMultipartUpload and
    UploadPart build their attrs the same way (`internal/s3/copyobject.go:128`,
    `internal/s3/multipart.go:130`, `:315`) and were not run.
  - radosgw stores the headers the request itself carries
    ([rgw_rest.cc:123-131](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L123-L131), [rgw_rest.cc:123-131](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L123-L131);
    [rgw_rest.cc:2263-2270](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L2263-L2270), [rgw_rest.cc:2280-2287](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L2280-L2287)),
    so it stores no Cache-Control for that request.
  - The sweep's F3. net/http answers a request line whose version is not 1.x with 505
    ([server.go:1062-1064](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1062-L1064), [server.go:1129-1143](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1129-L1143)), and a
    `Transfer-Encoding: chunked` sent as two fields with 501
    ([transfer.go:639-661](https://github.com/golang/go/blob/go1.27.3/src/net/http/transfer.go#L639-L661), [server.go:2098-2108](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L2098-L2108)): before any
    handler, so with no request id and in no metric. radosgw answers 400 to both, by reading:
    beast refuses a version other than 1.0 and 1.1 ([basic_parser.ipp:321-325](https://github.com/boostorg/beast/blob/boost-1.82.0/include/boost/beast/http/impl/basic_parser.ipp#L321-L325), [basic_parser.ipp:253-257](https://github.com/boostorg/beast/blob/boost-1.87.0/include/boost/beast/http/impl/basic_parser.ipp#L253-L257))
    and a second chunked Transfer-Encoding ([basic_parser.ipp:863-868](https://github.com/boostorg/beast/blob/boost-1.82.0/include/boost/beast/http/impl/basic_parser.ipp#L863-L868), [basic_parser.ipp:769-774](https://github.com/boostorg/beast/blob/boost-1.87.0/include/boost/beast/http/impl/basic_parser.ipp#L769-L774)), and
    radosgw answers what beast cannot parse with an empty 400
    ([rgw_asio_frontend.cc:274-288](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L274-L288),
    [rgw_asio_frontend.cc:275-289](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L275-L289)).
  - The sweep lists three more that it does not class as defects, and that
    `docs/differences.md` does not record either:
    - a Transfer-Encoding other than one `chunked`, such as `gzip`, `identity` or
      `gzip, chunked`, is 501, where beast reports no error for it ([basic_parser.ipp:877-890](https://github.com/boostorg/beast/blob/boost-1.82.0/include/boost/beast/http/impl/basic_parser.ipp#L877-L890),
      [basic_parser.ipp:783-796](https://github.com/boostorg/beast/blob/boost-1.87.0/include/boost/beast/http/impl/basic_parser.ipp#L783-L796));
    - a version 1.x other than 1.0 and 1.1 is served ([server.go:1129-1132](https://github.com/golang/go/blob/go1.27.3/src/net/http/server.go#L1129-L1132)),
      where beast refuses it;
    - bare LF line endings and a folded header line are served, which the sweep did not compare
      with beast.
- **Decision:** each is recorded and pinned by a spec, and none is changed. Reasons:
  - net/http writes the 505 and the 501 itself, with no hook before them;
  - the Pragma rewrite leaves no trace. The header map of a request that sent
    `Pragma: no-cache` alone is the map of one that sent both headers, so a handler that
    dropped the Cache-Control would drop it from a client that sent it;
  - undoing any of them takes a reader in front of net/http, which `docs/differences.md`,
    "Expectations other than 100-continue", declines for the 417 (`docs/differences.md:3524-3528`),
    for a reason that holds for these too;
  - a spec makes a Go release that changes one of these answers fail, where today it would
    pass unseen.
- **Behaviour:** `docs/differences.md` gains:
  1. an entry for the Pragma rewrite, beside "A chunked request's Transfer-Encoding reads as
     `chunked`": what is stored, by which ops, and what radosgw stores;
  2. the 505 and the 501 in "Header limit and malformed requests", beside the 431;
  3. the 501 for another transfer coding, and the other 1.x versions that are served;
  4. bare LF line endings and folded header lines, once the task has read what beast does with
     each at Boost 1.82.0 and 1.87.0. One that beast serves too is no difference and is not
     recorded.
- **Seams:** none.
- **Specs:** the gateway ones on both releases:
  - PutObject with `Pragma: no-cache` alone: the HEAD carries `Cache-Control: no-cache`, the
    difference, pinned; with both headers; with Pragma and another Cache-Control; with neither;
  - CopyObject under `x-amz-metadata-directive: REPLACE`, and CreateMultipartUpload through to
    the completed object: the same, so that what was read is run;
  - a request line naming `HTTP/2.0`, `HTTP/3.0` and `HTTP/0.9`: 505; `HTTP/1.2`: served;
  - `Transfer-Encoding: chunked` as two fields, and `gzip`, `identity` and `gzip, chunked`: 501;
  - bare LF line endings and a folded header line: served.
- **s3-tests (PP-19):** none: nothing is fixed, and a test of any of these would assert a
  difference from radosgw.
- **Exclusions and registry:** `docs/differences.md`, as Behaviour lists. No registry entry:
  radosgw is not at fault.
- **Depends on:** nothing. **PR:** one. **Brief:** about 20 lines.

## Order

| Wave | Tasks | Waits for |
|---|---|---|
| 1, in parallel | F1, F2, F10 | nothing |
| 2 | F3 | F2 |
| 3, in parallel | F4, F5 with F6, F7, F8, F12, F13, F14, F15, F17, F18, F19, F20 | F2 and F3 (F5 and F6 only F2; F13 after F4; F14 nothing; F15 after F13; F17 after F15; F18 after F17; F19 after F18; F20 nothing; F16 superseded by F18) |
| 4 | F9 | F8 |
| cluster, one at a time | F11 | F1, F3, F4, F10; after F5 to F7 |
| then | the deferred benchmark run (spec §9), then phase 2's S1 to S3 | every task of the waves above merged |
| sweep, in parallel | F21, F22, F23, F24, F25, F26, F27, F28, F29, F30, F31, F32 | no wave above and no phase 2 task (F23 after F22; F25 after F22; the header route of F22, and F23, after FD-10's ruling; F31 after F30) |

F11 is the plan's only cluster task in this unit; it takes the one-at-a-time cluster slot and the
controller's recording slot. F4 gets an adversarial review; no F task is an OLH driver task, so
none takes P3's Fable review. F21 to F32 were numbered on 2026-11-28, after phase 2 began: F21 to
F29 from the sweeps of 2026-11-21, and F30 to F32 from the frontend sweep of 2026-11-28. They are
maintenance of phase 1 code like the rest of the unit, but nothing waits for them except the
phase 2 gate, which requires zero open findings. F21 to F27, F30 and F31 get adversarial review.

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
| FX-15 | A SigV2 presigned request's x-amz-meta-* and session token can move between its headers and its query with the signature kept (new, F15-2) | v19.2.7, v20.2.4 | security | unreproduced | one meaning wherever the split falls; a name on both sides refused; exclusions | F17 |
