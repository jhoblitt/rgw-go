# Phase 2 plan set: index

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task by task. The unit files carry the tasks; this index carries what binds them all.

**Goal:** Phase 2 of `docs/superpowers/specs/2026-09-25-rgw-go-design.md` §9: versioning, lifecycle and bucket configuration, served from the same RADOS state radosgw reads and writes.

**Architecture:** Each phase 2 feature is built bottom up in rgw-go's existing layers:
- the class client (`internal/cls/...`);
- the emulator (`internal/testutil/fakerados`);
- the metadata types (`internal/meta`);
- the driver (`internal/driver`);
- the op (`internal/op`);
- the S3 handler (`internal/s3`);
- the admin API.

Every on-RADOS form is radosgw's at Squid v19.2.7 and Tentacle v20.2.4. The S3 behaviour above those forms is the correct S3 behaviour, with each difference from radosgw recorded as an exclusion.

**Tech Stack:** Go 1.27, go-ceph through the jhoblitt/go-ceph fork (`ceph_preview`), Ginkgo v2 and Gomega, counterfeiter fakes, fakerados, disposable rooket clusters.

**Spec:** `docs/superpowers/specs/2026-09-25-rgw-go-design.md`. §8's compatibility rule and §9's phase 2 paragraph bind every task.

## How this plan is written

The owner's rule, which settles PP-1: the plan carries behaviour and seams, not code. A task gives:
- its decisions and their reasons;
- the behaviour it must produce, with the radosgw lines it reproduces, cited at both tags;
- the interfaces at its seams;
- the specs that pin it;
- its order.

No fenced block runs past 8 lines, and every Ceph citation in the set is a link to its lines at the tag or commit it was read at, `[<basename>:<from>-<to>](https://github.com/ceph/ceph/blob/<ref>/<path>#L<from>-L<to>)` (CL-D1), which `hack/citecheck` resolves (PP-2, PP-17). Where a task's text and an implementer's compiler disagree, the stated behaviour and the seams bind; the implementer records the ruling.

The set's files:

| File | Unit | Tasks |
|---|---|---|
| `00-index.md` | this index, and the shared tasks S1 to S27 | 27 |
| `V-versioning.md` | the OLH protocol, versioned ops, ListObjectVersions, MFA delete | V0-V33 |
| `L-object-lock.md` | object lock: configuration, retention, legal hold, enforcement | L1-L5b |
| `C-lifecycle.md` | lifecycle configuration and the worker | C1-C16 |
| `B-bucket-config.md` | CORS, website, encryption configuration, ownership controls, public access block, policy status, POST object | BC-2 to BC-35 |
| `WP-write-path.md` | checksums, compression and placement on writes, `?layout`, admin otp | WP-1 to WP-21, WP-24 to WP-37 |
| `T-gate-tooling.md` | the s3-tests selection in Go, the include list, the harness, the phase 2 gate | T-tasks, with WP-22 and WP-23 |
| `F-floor-19.2.7.md` | the floor bump to Squid v19.2.7: phase 1 maintenance that lands before phase 2, and F21 to F32, phase 1 maintenance numbered from the sweeps of 2026-11-21 and 2026-11-28 | F1-F32 |
| `CL-citation-links.md` | Ceph citations as clickable links, in the docs and in Go comments | CL1-CL6 |
| `SM-service-map.md` | rgw-go's registration in the manager's service map: the go-ceph binding, the harness's radosgw readers, the gateway's metadata and status | SM1-SM6 |

The dependencies between the tasks are drawn in [dependencies.md](dependencies.md), one graph per unit.

Task ids are the research reports' (`research/research-*.md` beside this index), so every task traces to its verified research. The research cites v19.2.6, where it was done; the plan's units cite v19.2.7.

## Global Constraints

Every task's requirements include these. Values are copied from the spec and the owner's rules.

- **Floors:** Squid 19.2.7 or Tentacle 20.2.4, "feature level Tentacle where gateway-side" (spec §3, which F1 amends: the owner moved Squid from 19.2.6 on 2026-10-13). The two tags are `v19.2.7` and `v20.2.4`.
- **Layout:** "Bit-compatible with radosgw's RADOS layout. Same pools and namespaces, same object naming, same object-class index protocol, same versioned encodings" (spec §3).
- **Compatibility** (spec §8, the owner, 2026-10-13): "Compatibility means RADOS state, in both directions. ... It does not mean reproducing radosgw's defects."
  - rgw-go does the correct S3 thing as long as the RADOS state it writes stays compatible.
  - It prevents corruption of shared data even where radosgw allows it.
  - Each difference is an exclusion, listed in the parity check's known-differences file.
- **Defects** are public (spec §8). Each goes into `docs/ceph-upstream-bugs.md` with its class (security, data corruption, S3-correctness, cosmetic) and its reproduction status, which stays unreproduced until a running release shows the defect.
  - A possible security defect is also sent to the rgw-bug-reproduction session.
  - A serious defect is reported upstream once reproduced.
- **Findings** are fixed in the PR whose review found them. One truly outside the change becomes a numbered task before that PR merges (PP-12). The phase 2 gate requires zero open findings.
- **PRs:** unrelated changes never share a PR; a task states its PR split (PP-13).
- **Size:** one layer per task, and a brief one reviewer judges in one pass, under about 250 lines (PP-6).
- **Threat model:** each unit file states its threat model. Reviews judge a component against it, and adversarial review is for the spec's security boundaries only (PP-14).
- **Shell is glue:** parsing, validation and logic are Go with tests (PP-15).
- **s3-tests:** every rgw-go fix, where reasonable, carries an s3-tests test that covers it, merged into jhoblitt/ceph's `rgw-go-s3tests` branch as part of the task. The pin bump is its own change.
- **The next s3-tests pin bump** adds the lines that the tests landed since the pin need. BC-22 (Block and policy-status recording and gate) later deletes the bump's deselect lines and adds the known-differences lines its sub-bullet lists. The tests come from jhoblitt/ceph#10 (52de0d67ba8 and 5a0d460976c), #11 (f2508c087c2), #12 (ad55801ab22) and #13 (564c7ca60db). A deselect line is `s3tests/functional/test_s3.py::<name> # <calls>`, the calls that need the feature; a known-differences line is `^s3tests\.functional\.test_s3::<name>$`, under a reason comment that names its exclusion. A quoted reason is from the task report or review named after it.
  - **Deselect lines, at the bump.** rgw-go routes none of these ops until BC-19 and BC-21, and BC-22 records them:
    - `s3tests/functional/test_s3.py::test_block_public_condition_on_unlisted_key # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_block_public_anonymous_root_principal # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_restrict_public_anonymous_root_principal # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_anon_canned_write_ignore_public_acls # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_anon_owned_object_acl_ignore_public_acls # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_anon_owned_object_acl_block_public_acls # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_anon_put_object_block_public_acls # PutPublicAccessBlock`
    - `s3tests/functional/test_s3.py::test_get_fixed_source_ip_bucket_policy_status # GetBucketPolicyStatus`
    - `s3tests/functional/test_s3.py::test_public_block_misspelled_setting_refused # PutPublicAccessBlock, GetPublicAccessBlock, DeletePublicAccessBlock`
  - **V10's known differences, at the bump,** in both files, under one reason comment citing "PutBucketVersioning answers MalformedXML for a document that does not parse": rgw-go answers a Status or MfaDelete outside S3's values with 400 MalformedXML, and "radosgw answers 400 InvalidArgument at both tags" (V10's review). The bump also records the baselines in full (run.sh, "record the baselines in full"). The bump that brings in jhoblitt/ceph#12's tests lands after V10 serves PutBucketVersioning (jhoblitt/rgw-go#353): before that, both gateways fail those tests, and these lines would fail the comparison as stale.
    - `^s3tests\.functional\.test_s3::test_versioning_bucket_invalid_status$`
    - `^s3tests\.functional\.test_s3::test_versioning_bucket_invalid_mfa_delete$`
  - **BC-22** deletes the deselect lines above with its own, and adds:
    - in both files, under "A bucket policy is public as Amazon S3 judges it", `^s3tests\.functional\.test_s3::test_block_public_condition_on_unlisted_key$`: "rgw-go refuses a grant to everyone under aws:SecureTransport and radosgw stores it" (S3's report);
    - in both files, each under its own exclusion, `^s3tests\.functional\.test_s3::test_restrict_public_anonymous_root_principal$`. In Squid's, under "Squid clusters get Tentacle's checks outside the policy language": "Squid's radosgw fails it on the unenforced block". In Tentacle's, under "A bucket policy is public as Amazon S3 judges it": v20.2.4 serves the anonymous GetObject through `arn:aws:iam:::root` under RestrictPublicBuckets (`docs/ceph-upstream-bugs.md`, "Tentacle's is_public misses a fixed principal that admits the anonymous requester") (S3's report);
    - in Tentacle's file, under "A bucket policy is public as Amazon S3 judges it", `^s3tests\.functional\.test_s3::test_block_public_anonymous_root_principal$`: "v20.2.4 stored the policy under BlockPublicPolicy", which rgw-go refuses, as Squid's radosgw does (S3's report);
    - in both files, under "IgnorePublicAcls sets aside grants to the user anonymous and its ownership", `^s3tests\.functional\.test_s3::test_anon_canned_write_ignore_public_acls$` and `^s3tests\.functional\.test_s3::test_anon_owned_object_acl_ignore_public_acls$`, and under "BlockPublicAcls counts grants to the user anonymous and its ownership, but for an anonymous writer's own", `^s3tests\.functional\.test_s3::test_anon_owned_object_acl_block_public_acls$`: "rgw-go passes these and radosgw fails them" (S5's report). `test_anon_put_object_block_public_acls` gets no line, since both gateways pass it;
    - in Squid's file, under "A bucket policy is public as Amazon S3 judges it", `^s3tests\.functional\.test_s3::test_block_public_policy_with_principal$`: "rgw-go stores a grant to one account and Squid's radosgw refuses it"; and under "Squid clusters get Tentacle's checks outside the policy language", `^s3tests\.functional\.test_s3::test_block_public_restrict_public_buckets$`: "rgw-go enforces and Squid does not" (S3's report);
    - in both files, under "A PutPublicAccessBlock document is held to S3's schema", `^s3tests\.functional\.test_s3::test_public_block_misspelled_setting_refused$`: rgw-go refuses the misspelled `BlockPublicACLs` with 400 MalformedXML, and "radosgw answers 200", storing the setting false (BC-17's report);
    - under "A bucket policy is public as Amazon S3 judges it", the judgment GetBucketPolicyStatus reports: in both files, `^s3tests\.functional\.test_s3::test_get_fixed_source_ip_bucket_policy_status$`, where rgw-go reports a grant to everyone from 1.1.1.1/32 alone not public, and radosgw reports "IsPublic true for 1.1.1.1/32" on both releases; and in Squid's file, `^s3tests\.functional\.test_s3::test_get_nonpublicpolicy_principal_bucket_policy_status$`, where rgw-go reports a grant to one fixed account not public, and Squid's radosgw reports it public (S3's report). Before writing these policy-status lines, BC-22 captures Amazon S3's own GetBucketPolicyStatus answers, which no AWS source available to the sessions settles (BC-20's task review): the answer for a bucket with no policy, which the reviewer recalls, unverified, as 404 NoSuchBucketPolicy, and whether S3 counts the bucket ACL in IsPublic. Where AWS differs from the lines listed, the exclusion "A bucket policy is public as Amazon S3 judges it" decides, and BC-22 records it.
- **Go conventions:** go-conventions (Ginkgo v2 and Gomega specs, errors wrapped, slog). Every build, test and lint passes `ceph_preview`; cgo builds use the project CLAUDE.md's `CGO_CFLAGS` and `CGO_LDFLAGS`. No task ids in code.
- **Clusters:** only disposable rooket clusters (`make cluster-up RELEASE=squid|tentacle`), reached through `ROOKET_NAME=rgw-go-<release> rooket k`; never the ambient kubectl. A task marked "Cluster: yes" runs one at a time.
- **rgw-rs:** every material change to `docs/exclusions.md` or `docs/differences.md` is announced to the rgw-rs session.
- **Before phase 2's tasks start:** unit F moves the Squid floor to v19.2.7, and then spec §9 has the deferred benchmark run (the seam microbenchmark's final form, the pipeline baseline and the first gateway comparison) run on a quiet host.

## Review Focus

These are the five input classes the spec implies, no single task's own specs would reach, and that are most likely to bite an operator running a mixed fleet. Each names its owning task, which carries the spec.

1. **radosgw and rgw-go writing versions of one key at once.** Expected: no version is lost, both gateways list the same versions in the same order, and the OLH points at the newest. Owner: V5b, plus a mixed-writer case in V15b's gate.
2. **A versioned write that meets a reshard in progress.** Expected: the write completes, or retries after the reshard, with no orphaned instance entry and no OLH log left unapplied. Owner: V5b.
3. **Both lifecycle workers on one zone across a crash mid-bucket.** Expected: no object is expired twice, no newer version is expired by a stale pass, and a locked object is never expired. Owner: C9 and C10b, plus C13's mixed-worker gate.
4. **A checksum algorithm the other gateway cannot read.** Expected: refused at write, never stored, so a v20.2.4 radosgw never reads past its table. Owner: WP-6 and WP-8.
5. **A POST object form at its edges:** a boundary string inside the file data, a file field larger than memory, a `$key` condition with a `${filename}` key. Expected: AWS's answers, with bounded memory. Owner: BC-29 and BC-32.

## The owner's decisions

- **2026-10-13:** the compatibility rule above.
- **2026-10-13:** the model split. Opus implements every task and runs every task review. Fable, one subagent at a time, does the whole-plan review before execution and fix rounds 4 and 5 of a stuck task.
- **2026-10-13, the owner's answers to P1 to P5 and D1:**
  - **P1:** the time-driven lifecycle s3-tests, about 1,100 s of sleeps, run at the gate and on demand only. The lifecycle configuration and header tests run on every PR. This amends spec §9's per-PR set.
  - **P2:** admin object removal and bucket purge ignore object lock, as radosgw does, with an exclusion. Rook's bucket deletion relies on the purge.
  - **P3:** V5b (the OLH index protocol, `setOLH` and its races) also gets a Fable review after its task review. V5c, split from V5b for size and carrying D1's races, shares that one review. The other OLH tasks get the standard Opus review.
  - **P4:** ownership controls are built to v21's stored form (BC-28a to BC-28f). A floor radosgw ignores the attr, so the mixed-fleet ACL difference is an exclusion.
  - **P5:** all four v19.2.7 fixes that v20.2.4 lacks apply on both releases, each a Tentacle exclusion. They are the reshard retry, obj_lock in the bucket instance JSON, lifecycle's current-version `x-amz-expiration` and the SigV4 relaxation. For SigV4, the F task first shows that the relaxation does not reopen CVE-2026-54330 and quotes AWS's signed-header rule; if either fails, Tentacle keeps the stricter check and the owner sees that before the merge.
  - **D1 accepted:** atomic conditional writes on versioned buckets. A pending entry that no writer will remove delays that key's conditional writes by a bounded backoff and never refuses them. Such an entry is left by a crashed writer, or by radosgw's own noncurrent-version transition, until `rgw_olh_pending_timeout_sec` ages it out. Two racing conditional writers may both answer 412. This is recorded as an exclusion. (The owner accepted a one-hour refusal; the whole-plan review's N2 removed the refusal, and this line records the result.)

## Decisions

The controller's rulings on the research's open questions, under the owner's rules. Unit files refer to them by id.

| Id | Decision | Reason |
|---|---|---|
| D1 | A conditional write to a versioned bucket commits only if its condition held when its link landed. Each attempt evaluates the condition on the OLH head it reads and guards its init on the `olh.info` it read. While other writers' pending attrs remain, it also waits on them, up to a bound, then proceeds without that wait. After the link, the writer voids its write (unlinks it and answers 412) if another version or marker took an epoch between the one it read and its own, or if the version its If-Match matched is no longer listed (V5c). That post-link check carries the guarantee; two racing conditional writers may both answer 412. | radosgw's check-then-act loses updates under concurrency (research-versioning Q1); the guard keeps the RADOS form. A pending attr that no writer will remove, such as the one radosgw's own noncurrent-version transition leaves, delays conditional writes by the bound and never refuses them (review N2). |
| D2 | GET and HEAD of a delete marker's own version id answer 405 with `x-amz-delete-marker: true`. | S3's behaviour; it costs one bi_get on that path only (Q2). |
| D3 | `versionId` on PutObject, on CopyObject's destination and on CompleteMultipartUpload is ignored. | S3 has no such parameter (Q3). |
| D4 | CopyObject sends `x-amz-version-id` and `x-amz-copy-source-version-id`; `x-amz-version-id: null` is sent on versioned buckets where radosgw omits it. | S3's headers (Q4, Q5). |
| D5 | Versioned writes await the bucket index ADD completion in phase 2. | Correctness first; the extra round trip is measured before any per-shard queue (Q6). |
| D6 | `x-amz-delete-marker` on the 404 of a current delete marker on Squid too. | Tentacle feature level (Q7). |
| D7 | Phase 1's `list-type=2&versions` document stays as is. | It is not an S3 request; the phase 1 behaviour holds (Q8). |
| D8 | `docs/differences.md`'s two `bi_*` sentences are amended: `bi_get` is in scope for one version's index entry, on the versioned delete and on D2's 404. That is one read for a named instance, and up to three for `null` (the plain key, then the null delete marker's key, then the null data key, in the class's `read_key_entry` order). `bi_put`, `bi_list` and the repair paths stay out. rgw-rs is told. | Q9. |
| D9 | No upstream u64 cmpxattr now; the string-mode guard is recorded in docs/cgo-limitations.md. | Q10. |
| D10 | Both lifecycle workers may run on one zone, under radosgw's shard lock and claim protocol plus a claim re-check. | radosgw documents several LC threads per zone, and its locks arbitrate (lock-lifecycle Q1). |
| D11 | Current expiration on a versioning-suspended bucket writes a null delete marker. | radosgw's permanent delete loses data (Q2). |
| D12 | A retention period or date past what the stored form holds is refused; a default retention saturates at write time. | Fail closed and stay RADOS-compatible (Q3). |
| D13 | An expired retention counts as absent. | Q4. |
| D14 | Rules naming a cloud-s3 tier leave their bucket unclaimed, and the shard's head in place before it, so radosgw's worker transitions it. rgw-go's private walk past it wraps to the shard's first entry, so every other bucket of the shard is still processed on every pass (C-D4). Until C10b and C11 land, the worker leaves unclaimed any bucket with a rule it cannot apply (C-D16). | A mixed fleet keeps cloud transitions (Q5). |
| D15 | Every S3 response carries CORS headers once the request's bucket is loaded. | AWS's model; there is no per-release route table to keep (bucketconfig Q2). |
| D16 | Where the status or error code a client sees differs, rgw-go follows AWS. Where only document bytes differ that SDKs ignore, it keeps radosgw's. | The cross-gateway oracle stays byte-comparable (bucketconfig Q3). |
| D17 | One AWS-correct `IsPublic` judgment on both releases (task S3). | Squid's over-block and the three-key environment are defects (bucketconfig Q4). |
| D18 | A signed CORS preflight's signature is not checked, as radosgw and AWS do; phase 3's rate limiter must not charge OPTIONS CORS to the key's user. | bucketconfig Q5. |
| D19 | PutBucketEncryption is validated as AWS documents, with 400 MalformedXML otherwise; the stored form is unchanged. | bucketconfig Q6. |
| D20 | An include list, `hack/s3tests/include-phase2.txt`, runs the marker-excluded tests phase 2 serves; the T unit owns it. | bucketconfig Q7; writepath. |
| D21 | Writes accept CRC32, CRC32C, CRC64NVME, SHA1, SHA256 and SHA512 under AWS's names. They refuse MD5, XXHASH64, XXHASH128 (type ids 9-11, which a v20.2.4 radosgw reads past its table) and BLAKE3. XXHASH3 is refused with an exclusion: no new dependency in phase 2. | writepath Q1. |
| D22 | Squid serves single-object checksums and a part's own value; the multipart combination is Tentacle-only. | Its persistent forms are struct fields only Tentacle has (writepath Q2). |
| D23 | A streamed copy of an uncompressed source into a compressing placement is compressed, on both releases. | writepath Q3; the exclusion text of the earlier copy decision is rewritten. |
| D24 | Tentacle's CreateBucketConfiguration `BucketIndex` stays 501. | It sits in no phase (writepath Q4). |
| D25 | `?layout` is the object op RGWGetObjLayout, for users with the admin=read cap. | It exists only as the object op at both tags (writepath Q5). |

## Shared tasks

### Task S1: fakerados's rgw class as method tables

- **Layer:** emulator. **Files:** modify `internal/testutil/fakerados/cls_rgw.go`; create one empty table file per area to come (`cls_rgw_olh.go`, `cls_rgw_lc.go`). **Cluster:** no.
- **Produces:**
  - a method table type, a `name -> (handler, write flag)` entry list per file;
  - `RGWClass()` assembled from every table;
  - `RGWWriteMethods` derived from the write flags.
- **Behaviour:**
  1. `RGWClass()` dispatches by table lookup, not by a switch.
  2. A method absent from every table answers EOPNOTSUPP, as today.
  3. `RGWWriteMethods` equals today's list, so existing callers are unchanged.
  4. A name in two tables panics at assembly.
- **Specs:**
  - the existing `cls_rgw*_test.go` suites pass unchanged;
  - an unknown method is EOPNOTSUPP;
  - the derived write list equals the list at the base commit;
  - a duplicate name panics.
- **Why first:** PP-5. Versioning, lifecycle and the otp class all add class methods, and each adds a table file instead of editing one switch.
- **Depends on:** nothing. **PR:** one. **Brief:** about 80 lines.

### Task S2: route sets per feature and the Env slots

- **Layer:** handler and op. **Files:** modify `internal/s3/handler.go` (`NewHandler`, `buildHandler`, `unitHandlers`); create `internal/s3/routesets.go` and one stub route-set file per phase 2 feature; declare in `internal/op` the three interfaces phase 2's `Env` fields hold, with the signatures their units give: `MFAVerifier` (`internal/op/mfa.go`, V13), `LifecycleStore` with `SetEntry` and `RemoveEntry` (`internal/op/lifecyclestore.go`, C5), and `LayoutStore` (`internal/op/layout.go`, WP-20); and add the fields `Env.MFA`, `Env.Lifecycle` and `Env.Layout`. Implementations come from those tasks. **Cluster:** no.
- **Produces:**
  - `type routeSet struct { scope op.Scope; handlers map[string]HandlerFunc }`;
  - one function per feature returning `[]routeSet`, given what its handlers need (the env, and the authenticator for POST object);
  - `NewHandler` binding every set through the existing `bind`.
- **Behaviour:**
  1. Routing is unchanged.
  2. `bind` keeps its panics, now across sets: an unknown scope or name, and a route bound twice.
  3. A feature task later edits only its own route-set file.
- **Specs:**
  - an empty set binds nothing;
  - a set binding a route twice panics;
  - a set naming a route its scope never dispatches panics;
  - every phase 1 route still dispatches as before (the existing handler suites pass).
- **Why first:** PP-5. Every handler task in V, L, C, B and WP otherwise edits `bucket.go` and `handler.go`.
- **Depends on:** nothing. **PR:** one. **Brief:** about 80 lines.

### Task S3: one AWS-correct IsPublic

- **Layer:** policy (`internal/policy`). **Files:** modify `internal/policy/policy.go` (`Policy.IsPublic`) and its callers' arguments. **Cluster:** no.
- **Consumes:** `Policy`, `Semantics`. **Produces:** `func (p *Policy) IsPublic() bool`, with the same answer on both releases.
- **Behaviour** (D17):
  1. A policy is public exactly when AWS's definition says so: the S3 User Guide's "The meaning of 'public'", which lists the condition keys and values that make a wildcard-principal grant non-public.
     - The implementer reads that page and quotes the rules in the task's notes, citing the page. They are not written from memory (PP-2).
     - Each listed key and value becomes a spec row.
  2. This replaces the evaluation in radosgw's fixed three-key environment ([rgw_iam_policy.cc:1894-1898](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1894-L1898)).
  3. It also replaces Squid's counting of Allow statements without a wildcard principal ([rgw_iam_policy.cc:1900-1922](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1900-L1922), [rgw_iam_policy.cc:1935-1953](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1935-L1953)).
  4. The registry entries for both defects record the choice, and an exclusion states the difference.
- **Specs:**
  - a table over AWS's documented public and non-public policy examples, each answered the same under both releases' `Semantics`;
  - the phase 1 BlockPublicPolicy and RestrictPublicBuckets specs, updated where their expectation was a floor defect, each change citing its registry entry.
- **Consumers:** BC-18 (the block ops) and BC-20 (policy status); `internal/authz`'s public-bucket checks.
- **Depends on:** nothing. **PR:** one. **Brief:** about 120 lines.

### Task S4: fakerados refuses an unregistered class method at op setup

- **Layer:** emulator. **Files:** modify `internal/testutil/fakerados/cluster.go`, `steps.go` and `cls_rgw.go`, and the specs that register the rgw class. **Cluster:** no.
- **Why:** S1's review found that the fake checks only an op's class before its steps run, and refuses an unregistered method only when that method's own step runs. The OSD refuses the whole op first. `PrimaryLogPG::do_op` calls `maybe_init_op_info` ([PrimaryLogPG.cc:2008](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/PrimaryLogPG.cc#L2008), [PrimaryLogPG.cc:2051](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/PrimaryLogPG.cc#L2051)), and `OpInfo::set_from_op` answers EOPNOTSUPP for a method its class does not register ([osd_op_util.cc:189-196](https://github.com/ceph/ceph/blob/v19.2.7/src/osd/osd_op_util.cc#L189-L196), [osd_op_util.cc:189-196](https://github.com/ceph/ceph/blob/v20.2.4/src/osd/osd_op_util.cc#L189-L196)). That happens before the object is looked up and before any step runs, whatever the call's position or `FailOK` flag. The gap predates S1 and applies to every emulated class.
- **Produces:**
  - `(*Cluster).RegisterClassMethods(name string, fn ClassFunc, methods, writes []string)`; `RegisterClass` keeps today's behaviour, every name reaching `fn`;
  - `fakerados.RGWMethods`, every name the merged rgw tables hold, sorted, derived as `RGWWriteMethods` is.
- **Behaviour:**
  1. An op calling a method that its class registered through `RegisterClassMethods` without listing it fails with EOPNOTSUPP before any step runs, and changes nothing.
  2. A class registered through `RegisterClass` behaves as today.
  3. Every registration of the rgw class, including the specs' wrapping registrations, lists `RGWMethods` and `RGWWriteMethods`. A wrapper keeps the class's method set.
- **Specs:**
  - a table over the four shapes the review probed, each answering EOPNOTSUPP: a read op of the call alone on a missing object; a write op of the call alone on a missing object; a read op whose earlier `CmpXattr` fails; and `WriteFull` then the call flagged `FailOK` on a stored object, whose data stays unchanged;
  - a `RegisterClass` class still reaches its function for any name;
  - `RGWMethods` holds exactly the merged tables' names.
- **Consumers:** WP-17's otp class registers its method set from the start.
- **Depends on:** S1. **PR:** one. **Brief:** about 70 lines.

### Task S5: IgnorePublicAcls ignores Swift's public container grants

- **Layer:** acl and authz (`internal/acl`, `internal/authz`). **Files:** modify `internal/acl/eval.go` and the authz evaluator's ACL checks, their specs, and `docs/differences.md`. **Cluster:** for one spec, if the rooket clusters' radosgw serves Swift.
- **Why:** S3's review found that a public container grant written through Swift escapes IgnorePublicAcls, in radosgw at both tags and in rgw-go, which reproduces it. Two paths let it through:
  - the referer grants are read whatever IgnorePublicAcls says ([rgw_acl.cc:190-192](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_acl.cc#L190-L192), [rgw_acl.cc:190-192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_acl.cc#L190-L192)); rgw-go's `internal/acl/eval.go` reads them outside its `ignorePublicACLs` gate;
  - with `rgw_enforce_swift_acls`, true by default, an object read falls back to the bucket ACL's Swift flags with IgnorePublicAcls off ([rgw_common.cc:1598-1599](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_common.cc#L1598-L1599), [rgw_common.cc:1655-1656](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_common.cc#L1655-L1656)); rgw-go's object ACL check passes `false` there too.

  With all four public access block settings on, a container a Swift user made public with `.r:*` serves anonymous S3 GetObject, and anonymous ListBucket whenever the request sends a Referer. The registry records the radosgw side.
- **Behaviour** (AWS: IgnorePublicAcls "causes Amazon S3 to ignore all public ACLs on a bucket and any objects that it contains"):
  1. Under IgnorePublicAcls, no referer grant is read. A Referer is the client's to send, so every referer grant is a grant to everyone.
  2. Under IgnorePublicAcls, the Swift fallback ignores the bucket ACL's grants to AllUsers and AuthenticatedUsers and its referer grants, as the bucket and object checks do.
  3. Without IgnorePublicAcls, both paths answer as today.
  4. `acl.Policy.IsPublic` counts a grant to AllUsers or AuthenticatedUsers of any flag, Swift's included, and any referer grant, as public, so the policy status BC-20 serves does not call such a bucket private.
  5. An exclusion states the difference from radosgw on both releases.
- **Specs:**
  - under IgnorePublicAcls, a stored bucket ACL with a `.r:*` referer grant and an AllUsers READ_OBJS grant denies an anonymous GetObject, and an anonymous ListBucket with and without a Referer; without it, both are allowed as today;
  - a referer grant to one domain is ignored under IgnorePublicAcls;
  - IsPublic is true for an ACL with only Swift flags or only a referer grant;
  - if the clusters' radosgw serves Swift, one cluster spec: radosgw's Swift API sets `X-Container-Read: .r:*`, S3 sets all four block settings, and rgw-go refuses the anonymous read. If they do not, the implementer records why, and the stored ACL's bytes from such a container pin the specs instead.
- **Depends on:** nothing. **PR:** one. **Brief:** about 90 lines.

### Task S6: one choose_from

- **Layer:** meta, op and driver. **Files:** modify `internal/meta` (V2's `chooseFrom`, exported), `internal/op/adminuser_sub.go` and `internal/driver/writer.go`, and their specs. **Cluster:** no.
- **Why:** V2's review (N4) found three helpers reproducing radosgw's `choose_from` ([random_string.cc:33-42](https://github.com/ceph/ceph/blob/v19.2.7/src/common/random_string.cc#L33-L42), [random_string.cc:33-42](https://github.com/ceph/ceph/blob/v20.2.4/src/common/random_string.cc#L33-L42)): `meta.chooseFrom`, `op.randomFrom` and `driver.randAlnum`. radosgw casts each random `char` to `unsigned`, which sign-extends it on x86-64; `op.randomFrom` does so and the other two read the byte unsigned. For the 36- and 64-character tables the two reads pick the same characters, since both divide 2^32 - 256; for the 62-character plain table they differ.
- **Produces:** one exported helper in `internal/meta`, which op and driver call; the other two are deleted.
- **Behaviour:**
  1. The helper reads each byte as radosgw's cast does on x86-64, sign-extended, for every table.
  2. Each caller keeps its table and length, as its current specs pin.
- **Specs:** a byte table over the three table sizes, sign-extended reads included; the callers' existing specs pass unchanged.
- **Depends on:** V2. **PR:** one. **Brief:** about 40 lines.

### Task S7: one schema scanner for S3 XML request bodies

- **Layer:** library (`internal/xmltext`). **Files:** add the scanner to `internal/xmltext`; modify the five documents' scans, V10's `internal/s3/bucketversioning.go`, BC-17's `internal/acl/publicaccess.go`, BC-23's `internal/sse/xml.go`, BC-10's `internal/website/scan.go` and BC-28a's `internal/acl/ownership.go`, and their specs; and the PutPublicAccessBlock and PutBucketEncryption entries of `docs/differences.md` (Behaviour 4). V10's call site is in `internal/s3`, a handler package: an exception to the library layer, as V10 keeps its document's scan beside its handler, and the change there only swaps that scan for a call into the scanner. **Cluster:** no.
- **Why:** BC-10's review (Minor 4) found that four documents each carry a near-identical scan of their body's tokens before `xmltext.Parse` builds a tree, and BC-28a's ownership controls document added a fifth (BC-28a's re-review). Each refuses, as MalformedXML, much of the same structure: a declaration, whose internal subset can declare entities that expat expands; a prefixed element name; a namespace other than S3's; a second of an element its schema allows once; and, in BC-17, BC-23, BC-10 and BC-28a, unknown elements and attributes. Five guards for one rule drift apart as each document's review tunes its own: BC-28a's re-review found two of them over their own bound (Behaviour 3).
- **Produces:** one scanner over a document's table of the children each element holds, and which of them may repeat. A document adds its own checks of an element's text or children where that element ends, as BC-10's AWS constraints do.
- **Behaviour:**
  1. No document's answers change but for its error text: each keeps its refusals, their order, their Messages, and the leniency its task chose (V10 reads elements its schema does not name, where BC-17, BC-23, BC-10 and BC-28a refuse them).
  2. A refused body costs a read of its tokens up to the refusal, and no tree is built for it.
  3. Every document's client text in a refusal is cut by bytes before it is quoted, as `sse.quote` and `clip` (`internal/sse/xml.go:270-282`) and BC-28a's in `internal/acl/ownership.go` do, so each refusal's `Error()` is at most 512 bytes. A cut by runes, `%.64q` or `%.128s`, is not a bound: a non-printable rune quotes as up to ten bytes. That fixes two defects against their own specs:
     - BC-17's `internal/acl/publicaccess.go` cuts with `%.64q` (the attribute site at :191), and its spec asserts `< 512` (`internal/acl/publicaccess_test.go:366-378`, at :370); BC-28a's re-review probe gives 729 bytes for an xmlns of 64 × U+E0001, and 788 for a 64-byte attribute name with such a value.
     - BC-10's `internal/website/scan.go:76` cuts with `%.64q=%.64q`, and its bound is `maxRefusalText = 512` (`internal/website/fuzz_test.go:122`, and `internal/website/xml_test.go:328`); the probe gives 712 bytes for an xmlns of 64 × U+E0001.
  4. The PutPublicAccessBlock and PutBucketEncryption exclusions lose "so under the default the parser's limit is never reached" (`docs/exclusions.md:2556-2567` and `:2636-2647` at c02a9f8). Both ops ask for no chunked body, but `RGWOp::read_all_input` drops that argument and reads one ([rgw_op.cc:8629](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8629) and [rgw_op.cc:8760](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L8760), [rgw_op.h:216-228](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.h#L216-L228) at v19.2.7; [rgw_op.cc:9597](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9597) and [rgw_op.cc:9728](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L9728), [rgw_op.h:229-241](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L229-L241) at v20.2.4; docs/ceph-upstream-bugs.md, "RGWOp::read_all_input ignores its allow_chunked argument"), and `read_all_chunked_input` checks the option only after a read fills its buffer ([rgw_rest.cc:1506-1540](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.cc#L1506-L1540), [rgw_rest.cc:1506-1540](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.cc#L1506-L1540)). So under the 1 MiB default radosgw reads and parses a chunked body of up to 1,175,551 bytes, and rgw-go, which reads it as radosgw does (`op.ReadParamBody`), refuses one over 1 MiB. Each entry says so, as BC-28a's "A PutBucketOwnershipControls document is held to S3's schema" does.
- **Specs:** each document's existing specs and fuzz targets pass unchanged; the scanner's own table over the shared refusals, once per leniency; and, in each document's short-refusal table, the probe inputs: 64 × U+E0001 in an xmlns, and a 64-byte attribute name with such a value.
- **Depends on:** V10, BC-17, BC-23, BC-10, BC-28a. **PR:** one. **Brief:** about 130 lines.

### Task S8: a Deny that names NotAction refuses every action it does not name

Found by WP-25's adversarial review (M1), and numbered here before WP-25 merged (PP-12).

- **Layer:** policy (`internal/policy`). **Files:** the statement's action match in
  `internal/policy/statement.go` and its specs, `internal/policy/eval_test.go`;
  `docs/differences.md`; `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. A Deny statement that names `NotAction` applies to every action its NotAction does not name,
     as Amazon S3 applies it (the IAM JSON policy reference's NotAction element, cited at
     implementation), so a Deny written with NotAction alone refuses the actions it covers.
     radosgw's `Statement::eval` passes any action its `action` mask lacks
     ([rgw_iam_policy.cc:1222](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1222),
     [rgw_iam_policy.cc:1226](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1226)),
     and its parser fills that mask only from an `Action` element
     ([rgw_iam_policy.cc:642-656](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L642-L656)),
     so a statement naming only NotAction matches no action and such a Deny refuses nothing.
     rgw-go copies that today (`internal/policy/statement.go:111`).
  2. An Allow statement that names only NotAction keeps radosgw's evaluation and grants nothing.
     Taking Amazon S3's semantics there would grant on rgw-go what radosgw refuses on the same
     zone; that is the owner's decision, and until the owner makes it rgw-go differs only on the
     refusing side.
  3. A statement naming both Action and NotAction is answered as radosgw answers it, verified at
     both tags at implementation.
- **Specs:** per release, a bucket policy allowing `s3:*` and denying NotAction `s3:GetObject`
  refuses PutObject, DeleteObject and GetObjectAttributes and serves GetObject; the same Deny with
  NotAction naming several actions; a NotAction-only Allow grants nothing, as before; an explicit
  Deny of an action keeps refusing it.
- **Exclusions and registry:** a `docs/differences.md` entry, since rgw-go refuses where radosgw
  serves; a `docs/ceph-upstream-bugs.md` entry whose Upstream line cites
  [#73983](https://tracker.ceph.com/issues/73983), Fix Under Review, and
  [ceph/ceph#66099](https://github.com/ceph/ceph/pull/66099), closed unmerged on 2026-10-22.
- **Depends on:** nothing. **PR:** one. **Brief:** about 45 lines.

### Task S9: a Deny's condition holds where Amazon S3's or radosgw's evaluation holds

Found by L7's adversarial review (F2, F4), and numbered here before L7 merged (PP-12).

- **Layer:** policy (`internal/policy`). **Files:** condition evaluation in
  `internal/policy/condition.go`, the statement's use of it, and their specs;
  `docs/differences.md`; `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. A negated condition operator (StringNotEquals, StringNotEqualsIgnoreCase, StringNotLike,
     NumericNotEquals, DateNotEquals, ArnNotEquals, ArnNotLike, NotIpAddress, and any other
     negated operator rgw-go parses) on a key the request context lacks holds in a Deny
     statement, as the IAM User Guide's condition operators page says Amazon S3 evaluates it
     (cited at implementation). radosgw's `Condition::eval` answers the condition's IfExists flag
     for every absent key but Null and the ForAllValues operators
     ([rgw_iam_policy.cc:854-869](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L854-L869),
     [rgw_iam_policy.cc:873-889](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L873-L889)), and rgw-go copies it today.
  2. On Squid, whose Null ignores its values (the registry's "Squid's negated condition operators
     hold when any pair differs, and its Null ignores its values"), a Null condition in a Deny
     statement holds when Squid's answer or Amazon S3's holds.
  3. An Allow statement keeps radosgw's evaluation for both. Amazon S3's answer there would grant
     on rgw-go what radosgw refuses on the same zone; that is the owner's decision, with S8's
     NotAction Allow, and until the owner makes it rgw-go differs only on the refusing side.
- **Specs:** per release, each negated operator on an absent key and on a present one, in a Deny
  and in an Allow statement; Null true and false on an absent and a present key, in each; the
  IfExists forms unchanged.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go lines of the registry
  entries L7 adds for the absent-key rule.
- **Depends on:** L7. **PR:** one. **Brief:** about 40 lines.

### Task S10: a Deny's condition key names match without regard to case

Found by L7's adversarial review (F3), and numbered here before L7 merged (PP-12).

- **Layer:** policy (`internal/policy`). **Files:** the condition key's parse and the environment
  lookup (`internal/policy/env.go`, the condition parser) and their specs; `docs/differences.md`;
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. In a Deny statement, a condition's key name matches the request context's key whatever the
     case of either, as the IAM User Guide's condition element page says of Amazon S3 (cited at
     implementation). radosgw keeps the key as the policy spells it
     ([rgw_iam_policy.cc:476-483](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L476-L483),
     [rgw_iam_policy.cc:486-493](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L486-L493)) and finds it in an
     exact-match `std::unordered_multimap<std::string, std::string>`
     ([rgw_iam_policy.h:326](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L326),
     [rgw_iam_policy.h:345](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L345)), and rgw-go matches it today.
  2. An Allow statement keeps radosgw's exact match, as in S9, until the owner decides.
  3. Only the key's name folds: each operator keeps its own rule for the values.
- **Specs:** per release, an `aws:` and an `s3:` key spelled in another case, in a Deny and in an
  Allow statement; the exact spelling unchanged.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go line of the registry
  entry L7 adds for the key names.
- **Depends on:** L7. **PR:** one. **Brief:** about 30 lines.

### Task S11: a bound on an XML request body's elements and depth

Found by L7's adversarial review (F5), and numbered here before L7 merged (PP-12).

- **Layer:** the XML reader (`internal/xmltext`) and its specs; `docs/differences.md`.
  **Cluster:** no.
- **Security boundary:** the request path's parsing of untrusted bytes; adversarial review applies
  (PP-14).
- **Behaviour:**
  1. The tree builder refuses a document whose element count or nesting depth passes a bound, and
     stops building at the bound, with the error each op gives for a body it cannot parse. Today
     a body at `rgw_max_put_param_size` of nested or flat elements costs tens of milliseconds and
     60 to 110 times its size in allocation (measured at L7's head), and DeleteObjects and the
     lock subresources read such a body from a requester the permission check then refuses.
  2. The bounds admit every document an honest client sends within radosgw's own limits: the
     largest DeleteObjects, lifecycle configuration (`rgw_lc_max_rules`), CORS, tagging and
     website documents within `rgw_max_put_param_size`, measured at implementation; the bounds
     and their reasons are recorded.
  3. radosgw's `RGWXMLParser` sets no such bound on expat, installing only its element and
     character handlers ([rgw_xml.cc:219-221](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_xml.cc#L219-L221),
     [rgw_xml.cc:219-221](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_xml.cc#L219-L221)), so this is a difference.
- **Specs:** a document at each bound parses and one past it is refused, its allocation bounded
  (shown by an allocation count); each op's largest honest document parses.
- **Exclusions and registry:** a `docs/differences.md` entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 35 lines.

### Task S12: the object writes authorize the ACL and tags they set as Amazon S3 does

Found by S8's adversarial review (B-I1), and numbered here before S8 merged (PP-12).

- **Layer:** op (`internal/op`) and authz. **Files:** the permission checks of PutObject,
  CreateMultipartUpload, CopyObject's destination and the POST upload, and their specs;
  `docs/differences.md`; `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. A write that sets an object ACL (`x-amz-acl` or a grant header, or the POST form's `acl`) is
     also authorized as s3:PutObjectAcl, and one that sets tags (`x-amz-tagging`, or the POST
     form's `tagging`) as s3:PutObjectTagging, where Amazon S3's references require it (cited at
     implementation from the SDK's operation documentation, for each of the four operations; the
     PutObject reference requires both). radosgw evaluates s3:PutObject alone in each
     ([rgw_op.cc:3983](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3983), [rgw_op.cc:4192](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4192) for PutObject;
     [rgw_op.cc:6284](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6284), [rgw_op.cc:6951](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6951) for CreateMultipartUpload;
     [rgw_op.cc:5491](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5491), [rgw_op.cc:6054](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6054) for CopyObject;
     [rgw_op.cc:4600](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L4600), [rgw_op.cc:4893](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4893) for the POST upload), and rgw-go
     copies it today.
  2. This follows rgw-go's CreateBucket, which already authorizes its ACL headers as
     s3:PutBucketAcl too (the registry's "radosgw's CreateBucket takes an ACL other than private
     without s3:PutBucketAcl"). It refuses on rgw-go writes radosgw serves, on both the Allow and
     the Deny side, so it is recorded as a difference.
- **Specs:** per release and per operation, a requester allowed s3:PutObject alone setting an ACL
  and setting tags; the same with the second action allowed; an explicit Deny of s3:PutObjectAcl
  or s3:PutObjectTagging, by Action and by NotAction; a write that sets neither, unchanged.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go line of the registry
  entry S8 adds ("radosgw's PutObject sets the object ACL and tags its headers name under
  s3:PutObject alone").
- **Depends on:** S8. **PR:** one. **Brief:** about 40 lines.

### Task S13: CopyObject and CreateMultipartUpload evaluate s3:PutObject with the ACL and tag condition keys

Found by S12's adversarial review (Important 1), and numbered here before S12 merged (PP-12).

- **Layer:** authz. **Files:** the policy environment of CopyObject's destination and of
  CreateMultipartUpload, and their specs; `docs/differences.md`; the registry entry S12 adds.
  **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. CopyObject and CreateMultipartUpload evaluate s3:PutObject with the request's
     `s3:x-amz-acl`, `s3:x-amz-grant-*` and request-tag condition keys, as PutObject does.
  2. Whether Amazon S3 sets those keys for the two operations is decided at implementation from
     Amazon's documentation of the s3:PutObject condition keys. If no Amazon text decides it, the
     keys are set: a policy written to govern every write of an object must not be skipped by two
     of them.
  3. Under such a policy, rgw-go then refuses CopyObject and CreateMultipartUpload writes that
     radosgw serves; the differences entry records it.
- **Why:** radosgw adds those keys to the environment only for PutObject and the ACL operations,
  not for CopyObject or CreateMultipartUpload, and rgw-go copies it (S12's registry entry). So a
  bucket policy that conditions s3:PutObject on `s3:x-amz-acl`, for example one that requires
  `bucket-owner-full-control`, binds a PutObject but not a CopyObject or a
  CreateMultipartUpload carrying the same headers. S12's own s3:PutObjectAcl and
  s3:PutObjectTagging checks carry the keys on all three writes, so the gap is in policies written
  against s3:PutObject alone.
- **Specs:** per release, a bucket policy conditioning s3:PutObject on `s3:x-amz-acl`
  (StringEquals, and StringNotEquals `bucket-owner-full-control`) and on a request tag: PutObject,
  CopyObject and CreateMultipartUpload carrying the same headers give the same answer.
- **Depends on:** S12. **PR:** one. **Brief:** about 35 lines.

### Task S14: a Deny reads the request context Amazon S3 builds

Found by S9's implementation and task review, and numbered here before S9 merged (PP-12).

- **Layer:** authz and policy. **Files:** the request context (`internal/authz/env.go`), the Deny
  seam S9 adds to condition evaluation, and their specs; `docs/differences.md`; the registry
  entries S9 adds. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies (PP-14).
- **Behaviour:**
  1. A Deny statement's conditions read, besides radosgw's keys, the keys Amazon S3 sets on an S3
     request that radosgw leaves out or sets to another value, wherever rgw-go knows the value
     Amazon S3 gives:
     - aws:SecureTransport `false` on a request that is not secure, where radosgw sets the key only
       to `true`
       ([rgw_op.cc:865-867](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L865-L867),
       [rgw_op.cc:900-902](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L900-L902));
     - aws:CurrentTime and aws:EpochTime in Amazon S3's formats (the registry's "radosgw swaps
       aws:CurrentTime and aws:EpochTime");
     - the S3 keys the registry entry S9 adds for the class lists, such as s3:signatureversion,
       s3:authType and s3:x-amz-content-sha256.
  2. Each key's value comes from Amazon S3's documentation of it, cited at implementation. A key
     whose value no Amazon text decides stays as radosgw leaves it, and the differences entry
     names it.
  3. An Allow statement keeps radosgw's context, as S9 keeps its evaluation, until the owner
     decides. The added keys are visible to a Deny's conditions alone.
  4. Under a Deny whose condition holds only on Amazon S3's context, rgw-go refuses requests
     radosgw serves. The differences entry records it, and S9's paragraph on keys rgw-go does not
     set drops the keys this task sets.
- **Why:** S9 holds a Deny's negated operator on an absent key, as Amazon S3 does. But rgw-go's
  context lacks keys Amazon S3 always has, so such a Deny refuses every request it covers where
  Amazon S3 refuses only the requests whose value fails it. And a condition on a value radosgw
  never sets, such as aws:SecureTransport's `false`, never holds where Amazon S3's does.
- **Specs:** per release, in a Deny and in an Allow statement:
  - aws:SecureTransport on a secure and a plain request;
  - aws:CurrentTime and aws:EpochTime under a Numeric and a Date operator;
  - each added S3 key under a negated and a plain operator;
  - every Allow answer unchanged.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go lines of S9's two
  registry entries and of the time-swap entry.
- **Depends on:** S9. **PR:** one. **Brief:** about 40 lines.

### Task S15: CompleteMultipartUpload under a Deny on `s3:x-amz-acl`

Found by S13's implementation (concern 3) and task review (Minor 1), and numbered here before S13
merged (PP-12).

- **Layer:** authz, and under outcome (b) the multipart op. **Files:** the policy environment of
  CompleteMultipartUpload (`internal/authz/env.go`); under outcome (b), CreateMultipartUpload's
  record of the keys it carried (`internal/op/initmultipart.go`) and the completion's read of it;
  their specs; `docs/differences.md`; `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Why:**
  - Under a bucket policy that denies s3:PutObject with `StringNotEquals` `s3:x-amz-acl`
    `bucket-owner-full-control`, the Deny form of "require bucket-owner-full-control", rgw-go
    refuses every CompleteMultipartUpload. The completion sends no ACL header, its s3:PutObject
    check carries no `s3:x-amz-acl`, and S9 holds a Deny's negated operator on an absent key true,
    as the IAM User Guide says Amazon does
    ([reference_policies_elements_condition_operators.md:10](https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L10)).
    So no multipart upload completes under that policy, whatever its CreateMultipartUpload sent.
  - radosgw completes them. `RGWCompleteMultipart::verify_permission` adds only the encryption
    keys
    ([rgw_op.cc:6348-6363](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6348-L6363),
    [rgw_op.cc:7021-7036](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7021-L7036)),
    and `Condition::eval` answers a negated operator on an absent key false
    ([rgw_iam_policy.cc:854-869](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L854-L869),
    [rgw_iam_policy.cc:873-889](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L873-L889);
    the registry's "radosgw's negated condition operators are false on an absent key").
  - Under Amazon's own recommended form, an Allow with `StringEquals` `s3:x-amz-acl`
    `bucket-owner-full-control` as the only grant
    ([ensure-object-ownership.md:41-67](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/ensure-object-ownership.md?plain=1#L41-L67)),
    both gateways refuse every completion.
  - No pinned Amazon text says whether Amazon S3 sets `s3:x-amz-acl` on a completion, for
    example from the upload's CreateMultipartUpload. The user guide requires s3:PutObject "to
    complete a multipart upload"
    ([mpuoverview.md:125](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/mpuoverview.md?plain=1#L125))
    and names no condition key there, and none of its pages that name `s3:x-amz-acl` or
    `bucket-owner-full-control` says how a multipart upload is judged against them (read at
    that commit). If Amazon omits the key, it refuses as rgw-go does; if it carries the
    upload's, it completes an upload created with the ACL.
- **Behaviour:**
  1. **Establish Amazon's answer first:** whether Amazon S3 serves a CompleteMultipartUpload under
     the Deny above, for an upload whose CreateMultipartUpload sent
     `x-amz-acl: bucket-owner-full-control`, one that sent another ACL, and one that sent none.
     The sources are pinned AWS documentation (the S3 user guide and API reference, the IAM User
     Guide, the Service Authorization Reference), AWS knowledge-center articles cited with the
     date they were read, and an s3-test that records Amazon's answer (one asserting the
     completion and not marked `fails_on_aws`). One lead, which does not settle it alone:
     [ensure-object-ownership.md:69-77](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/ensure-object-ownership.md?plain=1#L69-L77)
     gives `aws s3 cp --acl bucket-owner-full-control` as the write that satisfies the Allow form,
     and names PutObject in the refusal. The CLI makes a multipart upload of a file above its
     multipart threshold; whether the page speaks for such a file is to be checked, not assumed.
     The task records each source read and its answer. If nothing settles it, the task says so
     and stops for the owner's ruling between (a) and (b). An answer that is neither (a) nor (b), such as completing an upload created
     without the ACL, also goes to the owner.
  2. **Outcome (a), refuse as now:** Amazon refuses, or the owner rules (a). It is fail-closed and
     follows the IAM rule. The completion's environment is unchanged. The differences entry of
     S9's absent-key rule, and S13's sentence that CompleteMultipartUpload adds no ACL key, state
     that under a negated Deny on `s3:x-amz-acl` no multipart upload completes on rgw-go, where
     radosgw completes it.
  3. **Outcome (b), the upload's keys:** Amazon completes the upload created with the ACL, or the
     owner rules (b). CreateMultipartUpload records the `s3:x-amz-acl` and `s3:x-amz-grant-*`
     keys it was judged with, those whose headers it sent, and the completion's s3:PutObject
     check carries them. An ACL header the completion itself sends still adds nothing, since the
     completion does not set the ACL.
     - The record keeps the RADOS formats byte-identical: the completed head carries exactly the
       attrs radosgw's completion writes, and radosgw still completes an upload rgw-go created.
       The upload stores its ACL as an encoded policy, not as the headers that made it
       (`internal/op/initmultipart.go`), so the task shows where the record lives, or how the
       keys are recovered, without a new attr reaching the head.
     - An upload with no record, one that radosgw created on the same zone among them, is judged
       as under (a): on an absent key.
     - Whether the recorded keys reach an Allow statement's conditions, or a Deny's alone as S14's
       added keys do, is the owner's, in the same ruling. If a Deny's alone, the Allow form above
       still refuses every completion, as radosgw does, and the differences entry says so.
  4. UploadPart is not changed here: both gateways judge it by its own headers, and an UploadPart
     that does not repeat the ACL header is refused under either policy form on both. Step 1
     records Amazon's answer for it too; one that differs becomes a numbered task (PP-12).
- **Specs:** per release, uploads created with `bucket-owner-full-control`, with `public-read`
  and with no ACL header, before the bucket policy is set (S13 judges a CreateMultipartUpload by
  its own headers), each completed:
  - under the `StringNotEquals` Deny: (a) 403 for all three; (b) 200, 403, 403;
  - under a positive Deny, `StringEquals` `s3:x-amz-acl` `public-read`: (a) 200 for all three;
    (b) 200, 403, 200;
  - under the Allow form as the only grant, as the owner's ruling sets it;
  - a completion that itself sends `x-amz-acl`, judged as one that sends none;
  - under (b), an upload with no record judged as under (a), and a completed head whose attrs are
    those radosgw's completion writes;
  - the encryption keys a completion adds, unchanged;
  - an s3-test of the completion under the Deny, where reasonable, with its pin move in this
    task's PR (PP-19); it is the natural home for S13's review's missing `StringNotEquals` case.
- **Exclusions and registry:** a `docs/differences.md` entry, or the lines above, for each
  answer that differs from radosgw's; the rgw-go line of the registry's "radosgw's negated
  condition operators are false on an absent key" names the completion's answer.
- **Depends on:** S13. **PR:** one. **Brief:** about 60 lines.

### Task S16: a Deny's typed operator over a repeated tag key

Found by S13's adversarial review (outside the PR, older than it), and numbered here before S13
merged (PP-12).

- **Layer:** tags and the S3 handlers, and policy for item 3. **Files:** the tag set's parse of a
  request (`internal/tags`); the tag reads of PutObject, CopyObject, CreateMultipartUpload and
  PutObjectTagging in `internal/s3`; for item 3, condition evaluation
  (`internal/policy/condition.go`); their specs; `docs/differences.md`;
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Why:**
  - A Deny of s3:PutObject with `NumericGreaterThan` `s3:RequestObjectTag/level` `5` is evaded by
    repeating the key: Squid serves `level=9&level=1` and stores level=9, and Tentacle serves
    `level=1&level=9`, alone or after eight other tags. Each release refuses the other order.
    It holds for PutObject on main since S12, and for CopyObject under REPLACE and
    CreateMultipartUpload since S13 gave them the tag keys.
  - radosgw adds every pair of `x-amz-tagging` to the environment, a repeated key as one key
    with several values
    ([rgw_op.cc:3970-3972](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3970-L3972),
    [rgw_op.cc:4179-4181](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4179-L4181)),
    and its Numeric, Date, Bool, Binary and IpAddress operators compare the one value
    `env.find` returns
    ([rgw_iam_policy.cc:880](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L880)
    and the typed cases from
    [rgw_iam_policy.cc:918](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L918),
    [rgw_iam_policy.cc:900](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L900)
    and from
    [rgw_iam_policy.cc:938](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L938)).
    rgw-go's `policy.Env.Find` reproduces which value each release reads (`docs/differences.md`,
    "A typed condition on a repeated key reads the value upstream's builds read").
  - radosgw takes a repeated key on every write: `check_and_add_tag` refuses only a full set,
    an empty key and an overlong key or value, and the set is a `std::multimap`
    ([rgw_tag.cc:23-35](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.cc#L23-L35),
    [rgw_tag.cc:23-35](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_tag.cc#L23-L35);
    [rgw_tag.h:13-16](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag.h#L13-L16),
    [rgw_tag.h:13-16](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_tag.h#L13-L16)).
    PutObjectTagging's body goes through the same check
    ([rgw_tag_s3.cc:32-53](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_tag_s3.cc#L32-L53),
    [rgw_tag_s3.cc:32-53](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_tag_s3.cc#L32-L53)).
    rgw-go's `tags.ParseHeader` keeps a repeated key too.
  - Amazon S3 refuses it. Its user guide: "Tags that are associated with an object must have
    unique tag keys"
    ([object-tagging.md:6](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/object-tagging.md?plain=1#L6)).
    Its PutObjectTagging reference names the error, `InvalidTag`, "if the tag did not pass input
    validation", pointing to that page
    ([service-2.json:1307](https://github.com/boto/botocore/blob/6411f5c81a991cc868f29eddf1a8eb0beb9da37d/botocore/data/s3/2006-03-01/service-2.json#L1307),
    botocore 1.35.0). No pinned text names the error a header write gives.
  - The registry's "radosgw evaluates a repeated x-amz-tagging key as one multivalued
    s3:RequestObjectTag condition key" covers the negated string operators only, not the typed
    read.
- **Behaviour:**
  1. **Item 1 applies: refuse the repeated key.** Refusing is Amazon's documented answer, so a
     tag set that names a key twice is refused on every write that takes one, before the
     request is authorized:
     - PutObject's `x-amz-tagging`;
     - CopyObject's, exactly when the copy stores the header's tags (`replaceTags` today, the
       tagging directive once WP-31 serves it);
     - CreateMultipartUpload's. It parses its header once authorized today, as radosgw does
       (S13's report); the refusal moves ahead of the check, so no request passes a check that
       read one value of the key;
     - PutObjectTagging's body. It adds no `s3:RequestObjectTag` key in either gateway, so the
       evasion does not reach it, but Amazon's rule is about the set an object holds.
     Each op answers as it answers a tag set that fails `check_and_add_tag` today: 400
     InvalidArgument on the header writes, as PutObject refuses an eleven-tag header, and 400
     InvalidTag on PutObjectTagging, as the reference names. A later write that takes a tag set,
     such as the POST upload's `tagging` field, refuses it the same way.
     Whether Amazon counts keys that differ only in case as one is taken from Amazon's text at
     implementation. If no text decides, they stay distinct, as both gateways hold them today,
     and S10's Deny reading covers them.
  2. **Item 2, holding a Deny's typed operator over every value of a request key, does not
     apply** to the request's tags: once item 1 refuses a repeated key, no `s3:RequestObjectTag`
     key reaches the environment with two values.
  3. A repeated key still reaches the environment from a tag set radosgw stored on the same
     zone, through `s3:ExistingObjectTag/<key>`, since the decode of a stored set must not refuse
     it. There a Deny's typed operator holds if any value of the key makes it hold, which fails
     closed. An Allow keeps radosgw's read, as S9 keeps its evaluation, until the owner decides.
- **Specs:** per release:
  - under a bucket Deny of s3:PutObject with `NumericGreaterThan` `s3:RequestObjectTag/level`
    `5`: `level=9&level=1`, `level=1&level=9` and the latter after eight other tags, on PutObject,
    CopyObject under REPLACE and CreateMultipartUpload, each refused 400 with nothing stored and
    no upload left; `level=9` refused 403 and `level=1` served, unchanged;
  - PutObjectTagging with a body that repeats a key: 400 InvalidTag, the object's tags unchanged;
  - a copy under COPY with such a header: as before, since it does not store the header;
  - an object whose stored tag set repeats `level`, under a Deny of s3:GetObject with
    `NumericGreaterThan` `s3:ExistingObjectTag/level` `5`: refused for either order of the
    values; under an Allow so conditioned, unchanged;
  - an s3-test of the header writes and of PutObjectTagging, with its pin move in this task's
    PR (PP-19).
- **Exclusions and registry:** a `docs/differences.md` entry for the refusal, which radosgw does
  not make, and for item 3; the entry "A typed condition on a repeated key reads the value
  upstream's builds read" drops the request's tags from the keys that repeat. A
  `docs/ceph-upstream-bugs.md` entry for the typed read of a repeated key, mechanism only,
  possibly security-relevant and sent to rgw-bug-reproduction (PP-4); the rgw-go line of the
  repeated-key entry above.
- **Depends on:** S13. **PR:** one. **Brief:** about 55 lines.

### Task S17: a Deny reads s3:signatureAge, s3:TlsVersion and s3:LocationConstraint

Found by S14's task review (Important 2), and numbered here before S14 merged (PP-12).

- **Layer:** auth, the S3 handler, op and authz, in one slice per key. **Files:** (a) the auth
  result and the request (`internal/op/identity.go`, `internal/op/request.go`), the verifier's
  results (`internal/auth/verifier.go`, `internal/auth/sigv4.go`,
  `internal/auth/browserupload.go`, `internal/op/browserupload.go`) and `s3.ApplyAuth`
  (`internal/s3/handler.go`); (b) `s3.ParseRequest` (`internal/s3/request.go`) and the request;
  (c) CreateBucket (`internal/op/bucketops.go`, `internal/s3/bucket.go`) and the request; for
  each, S14's Deny-only context in `internal/authz/env.go`; their specs; `docs/differences.md`;
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization, and (c) parses a client's document
  before the request is authorized; adversarial review applies (PP-14).
- **Why:** S14 leaves the three keys absent, as radosgw does, because rgw-go does not hold their
  values when it authorizes (S14's report, ruling 3):
  - SigV4's signing time stays inside auth. `parseV4Query` and `parseV4Header` parse
    `X-Amz-Date` into private credentials (`internal/auth/sigv4.go:137-150`, `:191-195`), and
    `op.AuthResult` (`internal/op/identity.go:92-139`), which `s3.ApplyAuth` copies into the
    request (`internal/s3/handler.go:540-562`), carries no time.
  - The request holds TLS as a bool (`internal/op/request.go:81`), set from
    `http.Request.TLS != nil` (`internal/s3/request.go:109`). v20.2.4's frontend holds the
    version as `TLS_VERSION`, for bucket logging alone
    ([rgw_asio_frontend.cc:326-334](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L326-L334)).
  - CreateBucket reads its `CreateBucketConfiguration` in `Params`, which Execute runs once
    VerifyPermission has passed (`internal/op/bucketops.go:139-155`, `:253-258`;
    `internal/s3/bucket.go:109-126`), as radosgw's `execute` calls `get_params` after
    `verify_permission`
    ([rgw_op.cc:3215-3244](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3215-L3244)
    and [rgw_op.cc:3458-3462](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3458-L3462),
    [rgw_op.cc:3443-3472](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3443-L3472)
    and [rgw_op.cc:3686-3690](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L3686-L3690)).

  radosgw sets none of the three at either tag: a search of its rgw sources finds
  `s3:LocationConstraint` and `s3:signatureAge` only in rows its keyword table comments out
  ([rgw_iam_policy_keywords.gperf:110](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy_keywords.gperf#L110),
  [:116](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy_keywords.gperf#L116);
  [rgw_iam_policy_keywords.gperf:111](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy_keywords.gperf#L111),
  [:117](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy_keywords.gperf#L117))
  and `s3:TlsVersion` nowhere. So AWS's own Denies are judged on an absent key. The one that
  denies "any Amazon S3 presigned URL request ... if the signature is more than 10 minutes old"
  ([using-presigned-url.md:48](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/using-presigned-url.md?plain=1#L48),
  [:62](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/using-presigned-url.md?plain=1#L62))
  and the one of TLS below 1.2 never hold, and the one of every Region but one refuses every
  CreateBucket, by S9's absent-key rule.
- **Behaviour:** each key is visible to a Deny alone, through S14's Deny-only context
  (`policy.Env.SetForDeny`), so every Allow answer is unchanged (S14's Behaviour 3). The keys
  go on the actions S14 puts its S3 keys on, every `s3:` action rgw-go checks (S14's report,
  ruling 5), but for `s3:LocationConstraint`.
  1. **(a) `s3:signatureAge`** is "the length of time, in milliseconds", for "Presigned URLs" and
     "Presigned POST", and "In Signature Version 2, this value is always set to 0" (the S3 API
     Reference's
     [bucket-policy-s3-sigv4-conditions](http://web.archive.org/web/20260520053457/https://docs.aws.amazon.com/AmazonS3/latest/API/bucket-policy-s3-sigv4-conditions.html)
     as archived on 2026-05-20, read 2026-11-21); "the age in milliseconds of the request
     signature"
     ([list_amazons3.md:76](https://github.com/awsdocs/amazon-s3-developer-guide/blob/e5e6bbd2771d39e8ce3736849548a94b98d5094b/doc_source/list_amazons3.md?plain=1#L76)
     in the S3 developer guide's archived source).
     - **Seam:** `op.AuthResult` carries the version of the signature auth verified and, for
       SigV4, its signing time, and `s3.ApplyAuth` copies both into `op.Request` as it copies
       `Route`. The verifier sets them where it builds its result
       (`internal/auth/verifier.go:167-170`), from the time it parsed. `AuthenticateBrowserUpload`
       sets them from the form's algorithm and its `x-amz-date`, which
       `op.BrowserUploadCredentials` gains (`internal/op/browserupload.go:14-30`), parsed as
       SigV4's basic form (`internal/auth/encoding.go:130`) and refused nowhere, since radosgw
       checks it nowhere (`internal/auth/browserupload.go:23-28`).
     - **Values:** whole milliseconds, in decimal, from the signing time to the clock reading S14
       takes for `aws:CurrentTime`: a SigV4 presigned URL's from its `X-Amz-Date`; a SigV4 form's
       from its `x-amz-date`, a field the form's signed policy must name, since BC-30's `Check`
       refuses a field no condition checked (`internal/postpolicy/policy.go:194-221`,
       `internal/postpolicy/env.go:49-61`); `0` for a SigV2 presigned URL and a SigV2 form. A
       SigV4 form whose `x-amz-date` does not parse, which an uploader can send only where the
       signed policy admits it, gives none. A route Amazon does not name gets none, as radosgw
       leaves it (S14's Behaviour 2): the header route of either version, an anonymous request
       and an unknown route.
     - **A signing time in the future,** which a presigned SigV4 URL may carry since auth checks
       no skew on that route (`internal/auth/sigv4.go:177-178`), counts by its distance from the
       clock reading. A Deny on `NumericGreaterThan` then refuses a URL dated further ahead than
       its bound as it refuses one signed longer ago, so whatever date a URL names, such a Deny
       serves it only within the bound of that date. No Amazon text read gives a value there.
     - POST object is not served until BC-32 and BC-33 (`internal/s3/postobject.go:10`). Whichever
       of (a) and BC-32 lands second fills the credentials' `x-amz-date` from the form.
  2. **(b) `s3:TlsVersion`** is "the TLS version used by the client", compared as a number:
     Amazon's examples deny "lower than 1.2, for example, 1.1 or 1.0" under `NumericLessThan`
     `1.2` and allow "higher than 1.1, for example, 1.2, 1.3"
     ([amazon-s3-policy-keys.md:288](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L288),
     [:291](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L291),
     [:306-307](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L306-L307),
     [:316](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L316)).
     - **Seam:** `op.Request` carries the negotiated version, which `ParseRequest` sets from
       `http.Request.TLS.Version` beside the bool.
     - **Value:** the version as `major.minor`, `1.2` or `1.3`, the two the frontend negotiates.
     - **None without TLS.** A plain-HTTP request carries no version, and nor does one a
       TLS-terminating proxy forwards, even under `rgw_trust_forwarded_https`, which makes
       `aws:SecureTransport` true from `Forwarded` or `X-Forwarded-Proto`
       (`internal/authz/env.go:232-244`) but passes no version. No Amazon text read gives a value
       for a request without TLS. So the Deny of TLS below 1.2 refuses neither, the TLS-only
       Deny on `aws:SecureTransport` being the one that refuses plain HTTP (S14), and a negated
       form, such as `NumericNotEquals` `1.3`, refuses both by S9's absent-key rule.
     - **Today the Deny of TLS below 1.2 never fires,** on rgw-go or radosgw: the key is absent
       and `NumericLessThan` is not negated. What keeps TLS 1.0 and 1.1 out is the frontend's
       floor, TLS 1.2, which rgw-go sets itself whatever `ssl_options` says
       (`internal/frontend/tls.go:39-42`; the one TLS listener, `internal/frontend/server.go:49`,
       `:163`), not Go's default. The task keeps that floor; (b) lets the policy see the version
       above it.
  3. **(c) `s3:LocationConstraint`** is "a specific Region"
     ([list_amazons3.md:61](https://github.com/awsdocs/amazon-s3-developer-guide/blob/e5e6bbd2771d39e8ce3736849548a94b98d5094b/doc_source/list_amazons3.md?plain=1#L61)),
     the constraint a CreateBucket's configuration names: Amazon's example grants
     `s3:CreateBucket` under `StringLike` `sa-east-1` and denies it under `StringNotLike`, "a
     create bucket request is denied if the location constraint is not `sa-east-1`"
     ([amazon-s3-policy-keys.md:351](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L351),
     [:369](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L369),
     [:380](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L380),
     [:403-404](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L403-L404)).
     - **Order:** CreateBucket reads and parses its document before its s3:CreateBucket check
       and after its anonymous refusal (`internal/op/bucketops.go:145-147`), so an anonymous
       request's body stays unread, as radosgw's does. The read keeps its bounds, the
       `rgw_max_put_param_size` of `readParamBody` (`internal/s3/bucket.go:149-174`) and S11's.
       PutObjectRetention is the pattern (`internal/op/objectretention.go:101-158`): a document
       that does not read or parse sets no key, and its error is kept and answered where
       `Params` answers it today, after the name and ACL checks (`internal/s3/bucket.go:95-118`).
     - **Seam:** `op.Request` carries the constraint for the environment, as it carries `List`
       and `Lock` (`internal/op/request.go:120-126`), set before the s3:CreateBucket check; the op
       keeps the parsed document, or its error, for `Params`.
     - **Value:** the zonegroup the constraint names, the text before a `:`, the part that picks
       the bucket's zonegroup (`internal/s3/bucket.go:120-126`,
       `internal/op/bucketops.go:398-411`), so that a Deny of one zonegroup is not passed by
       naming a placement after it. None for a request with no document, or with a constraint
       absent or empty, which creates the bucket in the gateway's own zonegroup. Set for
       s3:CreateBucket alone, the one rgw-go action of the two the Service Authorization
       Reference lists the key for (`docs/ceph-upstream-bugs.md`, "radosgw sets few of the
       condition keys Amazon S3 sets on S3 requests"), so the further checks a CreateBucket
       makes, s3:PutBucketAcl among them (`internal/op/bucketops.go:259-273`), do not carry it.
     - **Error precedence, a `docs/differences.md` decision:** a malformed document under a Deny
       whose condition holds on the key's absence, a negated operator by S9's rule, is refused
       403 AccessDenied, where radosgw, whose Deny does not hold on an absent key, answers 400
       InvalidArgument, as rgw-go answers it today (`internal/s3/bucket.go:238-249`). Every other
       answer keeps radosgw's order. Rejected: answering the 400 before authorization, which
       would tell a requester its policies refuse whether its document parses, and reorder
       every refused request.
- **Specs:** per release, in a Deny and in an Allow statement, each Allow answer unchanged:
  - (a) under a bucket Deny of s3:GetObject on `NumericGreaterThan` `s3:signatureAge` `600000`:
    a SigV4 presigned GET dated 11 minutes ago refused, one dated now served, one dated 11
    minutes ahead refused; a SigV2 presigned GET, a header-signed GET of either version and an
    anonymous GET served. Under `NumericEquals` `0` the SigV2 presigned GET refused; under
    `NumericNotEquals` `0` a header-signed GET refused, the key absent. Bounds of `1000` and
    `2000` against a URL 1.5 s old, holding and not (milliseconds). The auth result's version
    and time on each route, a SigV4 form's from its `x-amz-date`, none for one that does not
    parse, a SigV2 form's version alone. An Allow on `NumericLessThan` `600000` as the only
    grant refuses every route, as now.
  - (b) through `ParseRequest` and the evaluator, over TLS 1.2 and TLS 1.3 (the client's
    `MaxVersion`), over plain HTTP, and over plain HTTP from a trusted proxy that says https:
    under `NumericLessThan` `1.2` all served; under `NumericLessThan` `1.3` TLS 1.2 alone
    refused; under `NumericNotEquals` `1.3` TLS 1.2, plain and proxied refused. An Allow on
    `NumericGreaterThan` `1.1` as the only grant refuses all, as now.
  - (c) under an identity Deny of s3:CreateBucket on `StringNotLike` `s3:LocationConstraint`
    the zonegroup's api name (CreateBucket reads the identity's policies alone,
    `internal/op/bucketops.go:148`): that name served, and with a placement after it; another
    zonegroup's refused 403, ahead of the location error it gets today; no document, an empty
    constraint and Tentacle's document with a `BucketIndex` and no constraint refused. Under
    `StringEquals` another zonegroup's name, that name with a placement refused. A malformed
    document refused 403 under the `StringNotLike` Deny, and 400 InvalidArgument with no Deny
    and under the `StringEquals` one. An anonymous CreateBucket refused 403 with its body
    unread. The s3:PutBucketAcl check of a CreateBucket with `x-amz-acl` `public-read` without
    the key. An Allow on `StringLike` the name as the only grant refuses every create, as now.
- **s3-tests (PP-19):**
  - (a): a bucket Deny of s3:GetObject on `NumericLessThan` `s3:signatureAge` `600000`, which
    refuses a fresh signature, refuses a SigV4 and a SigV2 presigned GET and serves a
    header-signed one; one on `NumericEquals` `0` refuses the SigV2 presigned GET. radosgw
    serves all of them, so the test is `fails_on_rgw`.
  - (b): a Deny on `NumericLessThan` `s3:TlsVersion` `1.4` refuses a GET over TLS, which radosgw
    serves, so `fails_on_rgw`. The test skips on a plain-HTTP run, as the harness's is
    (`hack/s3tests/run.sh:273`).
  - (c) needs an identity policy, which s3-tests sets through IAM under the `user_policy`
    marker, and the harness leaves IAM out until phase 3 (`hack/s3tests/excluded-markers.txt:49-55`).
    Its test is written with that marker and `fails_on_rgw`, and runs once phase 3 lifts it.
  - Each PR that adds a test moves the pin, with the known-differences lines its run needs.
- **Exclusions and registry:** S14's `docs/differences.md` entry, "A Deny reads the condition
  keys Amazon S3 sets", takes each key with its value, and drops it from the keys it leaves
  absent; (a)'s value for a future signing time and (c)'s error precedence are recorded there.
  The rgw-go line of the registry's "radosgw sets few of the condition keys Amazon S3 sets on S3
  requests" names the keys set.
- **Depends on:** S14. **PR split:** (a), (b) and (c), each with its specs, its s3-test and pin
  move, and its differences and registry edits, in any order. **Brief:** about 110 lines.

### Task S18: a Date Deny reads every W3C time form

Found by S14's adversarial review (outside the PR, older than it), and numbered here before S14
merged (PP-12).

- **Layer:** policy (`internal/policy`). **Files:** a Deny's evaluation of the Date operators in
  `internal/policy/condition.go` and its specs; `docs/differences.md`;
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies (PP-14).
- **Why:**
  - A Deny whose Date condition is written with a UTC offset never holds: under
    `DateGreaterThan` `aws:CurrentTime` `2026-01-01T00:00:00+00:00`, or `-05:00`, a GetObject is
    served on main and at S14's head, on both releases (S14's adversarial review, probe P5).
    For a key the request holds as written, a Deny's Date operators convert through radosgw's
    reading (`internal/policy/condition.go:341-352`): `policy.AsDate` ends a time only at an
    upper-case `Z` (`internal/policy/convert.go:84-102`, `internal/iso8601/iso8601.go:84-86`),
    and the typed operators skip a condition value it cannot convert and are false where it
    cannot convert the key's value (`internal/policy/condition.go:1290-1327`).
  - radosgw reads them the same way at both tags: `as_date` hands a value that is not a number
    to `from_iso_8601`, whose `time_end` takes `Z` alone
    ([rgw_iam_policy.h:384-401](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.h#L384-L401),
    [rgw_iam_policy.h:403-420](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.h#L403-L420);
    [iso_8601.cc:74-78](https://github.com/ceph/ceph/blob/v19.2.7/src/common/iso_8601.cc#L74-L78),
    [iso_8601.cc:74-78](https://github.com/ceph/ceph/blob/v20.2.4/src/common/iso_8601.cc#L74-L78)),
    and its Date operators, `DateEquals` to `DateGreaterThanEquals`, skip such a value
    ([rgw_iam_policy.cc:940-959](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L940-L959),
    [rgw_iam_policy.cc:961-979](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L961-L979)).
    The registry's "radosgw's date conditions read no UTC offset" records it.
  - IAM takes "one of the W3C implementations of the ISO 8601 date formats" or epoch time
    ([reference_policies_elements_condition_operators.md:131](https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_elements_condition_operators.md?plain=1#L131)),
    whose time zone designator is "Z or +hh:mm or -hh:mm"
    ([W3C-NOTE-datetime.html:165](https://github.com/exelearning/iteexe/blob/3f4452780a4b2cfd2bc9d9152e92dfd86d2ff0cc/exe/webui/docs/W3C-NOTE-datetime.html#L165),
    as `docs/differences.md` cites the note), and its policy checks name
    `YYYY-MM-DDThh:mm:ssTZD (1997-07-16T19:20:30+01:00)`
    ([access-analyzer-reference-policy-checks.md:1963](https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L1963)).
  - rgw-go already reads those forms as Amazon S3 does, but only for a key the request holds in
    another case than the condition's (S10): `amazonAnswer` and its `amazonDates`
    (`internal/policy/condition.go:373-450`, `:761-794`), whose `amazonInstantValue` reads a
    date and time with an offset as the instant it names (`:863-916`).
- **Behaviour:**
  1. In a Deny, a Date operator over a key the request holds as written holds where radosgw's
     evaluation holds, or where Amazon S3's answer over that key, S10's `amazonAnswer`, is
     defined and holds. That reads an offset in the condition's values and in the key's value
     alike, so a request's or an object's tag written with one is read too, and `DateNotEquals`
     holds when no value names the key's instant.
  2. Where Amazon S3's answer is undefined, radosgw's evaluation alone decides, as it does for
     every other typed operator on a key held as written. It is undefined in the cases
     `docs/differences.md`'s "A Deny's condition key names match whatever their case" lists:
     where the readings of a date without a time, or of four digits, differ; for a value in no
     W3C form; and for a key holding several values. So the task adds a hold only where the IAM
     text decides one. It differs from the other-case path, where a condition with no answer
     holds, because there radosgw finds no key.
  3. **Which forms hold:** epoch seconds and every W3C form: a date and time with `Z`,
     `+hh:mm` or `-hh:mm`, to the minute, to the second or with a fraction of it; and a date, a
     year and month or a year, which Amazon S3's answer reads as any instant of it, as
     `docs/differences.md` says. A lower-case `t` or `z` is in none of them. It has no Amazon
     answer, and radosgw converts nothing that holds one, so a condition does not hold on such
     a value, in a Deny as in an Allow, on either gateway. The policy checks' wider "YYYY-MM-DD
     or other ISO 8601 date/time format"
     ([access-analyzer-reference-policy-checks.md:2198](https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/access-analyzer-reference-policy-checks.md?plain=1#L2198))
     names no further form. Rejected: holding such a condition, as the other-case path holds one
     with no answer, which would refuse every request at every time for one mistyped letter;
     and reading a lower-case `z` as `Z`, a form no Amazon text names.
  4. An Allow keeps radosgw's evaluation, as S9 keeps it.
- **Specs:** per release, in a Deny and in an Allow statement, under each of the six Date
  operators, on `aws:CurrentTime` and `aws:EpochTime` in S14's Deny context and in radosgw's
  environment, and on a request tag:
  - a condition value with `Z`, `+00:00`, `-05:00`, and an offset that moves the instant across
    the request's time (a clock face ahead of it naming an instant behind it), each to the
    minute, to the second and with a fraction;
  - a lower-case `t` and a lower-case `z`, holding in neither statement;
  - a date alone, four digits and epoch seconds, unchanged;
  - the key's value written with an offset; the same key in another case, unchanged (S10);
  - `DateNotEquals` whose only value has an offset, which radosgw's half holds on Tentacle and
    not on Squid;
  - every Allow answer unchanged.
- **s3-tests (PP-19):** a bucket Deny of s3:GetObject on `DateGreaterThan` `aws:CurrentTime` an
  hour before the request, written with `+00:00`, with `-05:00`, and as a clock face ahead of
  the request whose offset names that past instant, each refused, where radosgw serves them, so
  `fails_on_rgw`; the same instant with a lower-case `z`, served by both. The task moves the pin
  with the known-differences lines.
- **Exclusions and registry:** `docs/differences.md`: the entry "A Deny's condition holds where
  Amazon S3's evaluation or radosgw's holds" adds the Date operators to the cases where a key
  held as written takes Amazon S3's answer, and says a lower-case `t` or `z` holds nowhere; "A
  Deny's condition key names match whatever their case" points to it. No registry entry is
  added: radosgw reads the offset forms as `policy.AsDate` does, which "radosgw's date
  conditions read no UTC offset" records at both tags, and that entry's rgw-go line says a Deny
  reads them for a key held as written too.
- **Depends on:** S14, which changes the same Deny evaluation (`policy.Statement`'s conditions
  and `policy.Condition.EvalFor`). **PR:** one. **Brief:** about 50 lines.

### Task S19: a write's s3:PutObjectTagging check carries its tags whatever its query holds

Found by the authorization sweep of 2026-11-21 (F-A), on main, and numbered here (PP-12). It
overlaps public tracker [#82944](https://tracker.ceph.com/issues/82944), which reports the same
keys missing from radosgw's s3:PutObject check, the gap S13 closed on rgw-go.

- **Layer:** authz and op. **Files:** the policy environment (`opKeys`, `internal/authz/env.go`)
  and the write's check (`verifyACLAndTags`, `internal/op/writeacltags.go`), their specs;
  `docs/differences.md`. **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - `opKeys` gives an s3:PutObjectTagging check the `s3:RequestObjectTag/<key>` keys of the
    `x-amz-tagging` header only when the query has no `tagging` key
    (`internal/authz/env.go:616-625`). The test is meant to tell PutObjectTagging's own op,
    whose tags are in its body, from a write's check of its header.
  - The query does not say which op a request is. A POST of an object dispatches on `uploadId`
    and then `uploads`, and `tagging` is no row of that method
    (`internal/s3/dispatch.go:296-304`), so a CreateMultipartUpload may carry the key.
  - CreateMultipartUpload checks s3:PutObjectTagging for its header through
    `verifyWriteHeaders` (`internal/op/initmultipart.go:81-86`,
    `internal/op/writeacltags.go:103-127`) and stores the header's tags on the upload
    (`internal/op/initmultipart.go:146-148`). With the key in its query that check is evaluated
    without the request-tag keys, while the same request's s3:PutObject check carries them
    (`writeKeys`, `internal/authz/env.go:101-119`).
  - The check is rgw-go's own (S12): radosgw's CreateMultipartUpload is chosen the same way
    ([rgw_rest_s3.cc:4894-4900](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L4894-L4900),
    [rgw_rest_s3.cc:5421-5427](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L5421-L5427)) and checks s3:PutObject alone
    ([rgw_op.cc:6274-6289](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6274-L6289), [rgw_op.cc:6941-6956](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6941-L6956)).
- **Decision:** a check's keys are chosen by the op it is made for, which its caller knows, and
  never by a query key the dispatch of that method does not read. Reason: the query is the
  requester's to write, and the op is dispatch's to choose.
- **Behaviour:**
  1. A write's s3:PutObjectTagging check carries the `s3:RequestObjectTag/<key>` keys of the
     tags the write stores, whatever its query holds: PutObject, CreateMultipartUpload,
     CopyObject under the rule WP-31 gives it, and later the POST upload.
  2. The tagging op's own check is S24's.
  3. No other check changes.
- **Seams:** the caller of the s3:PutObjectTagging check says which check it is, a write's or
  the tagging op's, to the environment's builder; the names are the implementer's.
- **Specs:** per release:
  - under a bucket Deny of s3:PutObjectTagging with a positive operator on a request tag, and
    under an Allow so conditioned as the only grant, with `Null`, an `IfExists` operator and a
    `ForAllValues` one: a CreateMultipartUpload with a `tagging` key in its query and one
    without answer alike, and a refused one leaves no upload;
  - PutObject and CopyObject, unchanged;
  - a walk of every place the environment or the tag rule is chosen from the request's shape
    (`opKeys`, `writeKeys`, `tagRule`, `isCopyObject`, `isCreateMultipartUpload`) against the
    dispatch table: a query key the method's dispatch does not read changes no check's keys.
- **s3-tests (PP-19):** a CreateMultipartUpload whose query also holds `tagging`, under a bucket
  Deny of s3:PutObjectTagging on a request tag its header sets, is refused. radosgw makes no
  such check, so the test is `fails_on_rgw`. The task moves the pin with the known-differences
  lines.
- **Exclusions and registry:** `docs/differences.md`, "A write's ACL and tag headers need
  s3:PutObjectAcl and s3:PutObjectTagging", already promises the keys and needs no change beyond
  a sentence that the query plays no part. No registry entry: the check is not radosgw's.
- **Depends on:** WP-31, which changes the same check and its caller. **PR:** one. **Brief:**
  about 35 lines.

### Task S20: DeleteObjects judges each key with the key's tags

Found by the authorization sweep of 2026-11-21 (F-B and R-2), on main, and numbered here
(PP-12).

- **Layer:** authz. **Files:** the tag rule (`internal/authz/tagrule.go`) and its specs, the
  DeleteObjects specs; `docs/differences.md`; the rgw-go line of the registry entry the registry
  sweep adds. **Cluster:** no.
- **Security boundary:** request authorization; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - A single DeleteObject adds the object's tags to the environment when a policy names
    `s3:ExistingObjectTag` or `s3:ResourceTag` (`internal/authz/tagrule.go:72-80`).
  - DeleteObjects' check of each key (`VerifyBucketPermissionIn`,
    `internal/op/deleteobjects.go:417-425`) is the explicit-bucket form, for which `tagRule`
    adds an object's tags only to a read's actions (`internal/authz/tagrule.go:45-54`).
  - So each key's s3:DeleteObject or s3:DeleteObjectVersion is judged with no existing-tag key.
    A Deny under a positive operator on such a key does not hold where DeleteObject's holds. A
    Deny under a negated operator holds for every key, by S9's absent-key rule, where
    DeleteObject's holds only for the keys whose tag fails it.
  - radosgw is the same on the positive side at both tags:
    `RGWDeleteMultiObj::verify_permission` adds the tags of the request's object, which a
    multi-object delete does not name ([rgw_op.cc:6774-6786](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6774-L6786),
    [rgw_op.cc:7707-7719](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7707-L7719)), and each key is evaluated on that environment
    ([rgw_op.cc:6837-6847](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6837-L6847), [rgw_op.cc:7765-7775](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7765-L7775)),
    where `RGWDeleteObj::verify_permission` adds the object's
    ([rgw_op.cc:5138-5156](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5138-L5156), [rgw_op.cc:5528-5546](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L5528-L5546)).
- **Decision:** each key of a DeleteObjects is judged as a DeleteObject of that key is. Reason:
  one policy gives one answer whichever op deletes, and the tags are read only when a policy
  names one.
- **Behaviour:**
  1. For the explicit-bucket form, s3:DeleteObject and s3:DeleteObjectVersion take the key's
     tags as needed: the key's head is read only when the bucket's policy or one of the
     identity's names an existing-object or resource tag, as `tagAdder` decides for the single
     delete (`internal/authz/tagrule.go:215-260`).
  2. A key named with a version reads that version's tags, and one named without reads the
     current version's, as `objectFor` reads them (`internal/authz/tagrule.go:262-286`).
  3. A key whose tags a policy needs and that cannot be read is refused, never deleted, and its
     entry answers as a refused key does.
  4. A missing key adds no tag and is answered as today.
  5. A bucket whose policies name no tag reads nothing more than today.
- **Seams:** none new: `tagRule`'s explicit-bucket case.
- **Specs:** per release, through the handler and the evaluator:
  - under a bucket Deny of s3:DeleteObject with a positive operator on an existing tag: a key
    that carries the tag answers AccessDenied in the result and stays; a key that does not is
    deleted; DeleteObject of each answers the same;
  - under a Deny with a negated operator: a key whose tag satisfies the policy is deleted, and
    one that does not is refused;
  - under an Allow so conditioned as the only grant; with a version named;
  - a bucket with no tag-naming policy: no head read for the check, counted on the store;
  - tags that cannot be read: the key refused and kept.
- **s3-tests (PP-19):** a DeleteObjects under a bucket Deny of s3:DeleteObject on an existing
  tag answers AccessDenied for the tagged key and leaves it. radosgw deletes it, so the test is
  `fails_on_rgw`. The task moves the pin with the known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry beside "DeleteObjects checks
  s3:BypassGovernanceRetention for each key, on the key's ARN"; the rgw-go line of the registry
  entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 35 lines.

### Task S21: a request reads its bucket's policy once

Found by the authorization sweep of 2026-11-21 (F-C), on main, and numbered here (PP-12).

- **Layer:** authz and op. **Files:** the evaluator's preludes (`internal/authz/evaluator.go`,
  `internal/authz/load.go`), the request (`internal/op/request.go`), their specs. **Cluster:**
  no.
- **Security boundary:** the request path, what an untrusted request can make the gateway
  compute; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - No parsed policy is kept. `op.BucketPolicyFor` parses the stored text on every call
    (`internal/op/policystatus.go:90-103`), and every `Verify` entry of the evaluator reaches
    it (`bucketPolicy`, `internal/authz/load.go:27-29`; `requestBucket` and `explicitBucket`,
    `internal/authz/evaluator.go:409-460`).
  - DeleteObjects makes the check once for each key (`internal/op/deleteobjects.go:417-425`),
    up to `rgw_delete_multi_obj_max_num` keys (`:369-380`) and `rgw_multi_obj_del_max_aio` at
    once (`:385-393`). Each check also decodes the identity's policies, the bucket's ACL and
    its public-access block, and the governance bypass adds an evaluation for each key that
    exists (`bypassAllowed`, `internal/op/objectlock.go:136-146`).
  - The op's own permission check refuses only the marked refusals
    (`internal/op/deleteobjects.go:136-141`), so a key whose check refuses still costs its
    parse.
  - A bucket policy may be as large as `rgw_max_put_param_size`
    (`internal/s3/bucketsubres.go:50-66`). So the work of one small request grows with the
    product of its keys and the policy's size.
  - radosgw parses the bucket's policy once for a request
    ([rgw_op.cc:598-615](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L598-L615), [rgw_op.cc:628-645](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L628-L645)) and each
    key's check reads it ([rgw_op.cc:6837-6847](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L6837-L6847),
    [rgw_op.cc:7765-7775](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L7765-L7775)).
- **Decision:** what authorizes a request on a bucket is read once for that request and that
  bucket record. Reason: it bounds a request to one parse for each bucket it names, as radosgw
  is bounded, with no cache to invalidate. Rejected: a cache of parsed policies across requests,
  which needs the metadata cache's invalidation and is a performance change, not this fix; S22
  bounds the cost of the one parse that remains.
- **Behaviour:**
  1. The parsed bucket policy, the bucket's ACL, its public-access block and the identity's
     policies are read once for a request and a bucket record, and every check of that request
     on that record uses them: the request's own bucket check, DeleteObjects' check of each key,
     and each bypass check.
  2. Every answer is unchanged: the same refusals, marked or unmarked as today.
  3. The inputs belong to the record they were read from. A check on a record read again, as a
     retried bucket write reads one, reads them again.
- **Seams:** the request holds the inputs it has read, keyed by the bucket record, and both
  preludes (`requestPrelude`, `internal/authz/evaluator.go:345-358`; `explicitPrelude`, `:364`)
  take them from there. It is safe for the concurrent checks DeleteObjects makes.
- **Specs:** per release:
  - a DeleteObjects of 1, 10 and 100 keys parses its bucket's policy once, counted by a hook on
    the parse, with and without the bypass header on an object-lock bucket;
  - the existing answers of DeleteObjects, of a copy with a source in another bucket and of a
    retried bucket write, unchanged;
  - the per-key checks under the race detector.
- **s3-tests (PP-19):** none: the cost is not something an S3 response shows.
- **Exclusions and registry:** none: radosgw parses once, and so does rgw-go after this task.
- **Depends on:** nothing. **PR:** one. **Brief:** about 30 lines.

### Task S22: a policy parse costs time linear in its document

Found by the authorization sweep of 2026-11-21 (F-E), on main, and numbered here (PP-12).

- **Layer:** policy. **Files:** `internal/policy/parse.go` (`addPrincipal`, `addResource`) and
  its specs. **Cluster:** no.
- **Security boundary:** the request path: a bucket's owner writes the document and every
  requester of the bucket makes the gateway parse it; adversarial review applies (PP-14).
- **Why,** mechanism only:
  - The parser adds each principal and each resource to its statement through a scan of what
    the statement already holds (`internal/policy/parse.go:547-571`), so a statement of n
    distinct principals or resources costs about n²/2 comparisons to parse.
  - A stored bucket policy is parsed for each request that reaches its bucket's check (S21),
    and PutBucketPolicy takes a document as large as `rgw_max_put_param_size`
    (`internal/s3/bucketsubres.go:50-66`). The sweep measured seconds for one parse at that
    size.
  - radosgw holds a statement's principals and resources in sorted sets, as
    `internal/policy/parse.go:538-546` and `:562-565` say of it; its cost was not measured.
- **Decision:** membership is tested through a set beside each list. Reason: the lists keep
  their order and their contents, which evaluation and the dumps read, so no answer and no byte
  changes.
- **Behaviour:**
  1. A parse takes time linear in the document, up to a logarithmic factor.
  2. What a statement holds is unchanged: the first of the principals radosgw's set treats as
     equal is kept (`docs/ceph-upstream-bugs.md`, "radosgw drops all but the first OIDC
     provider or Service principal in a statement"), and a repeated ARN is held once.
- **Seams:** none: the two adders.
- **Specs:**
  - the parser's existing specs, unchanged;
  - generated statements with repeated and distinct principals and resources parse to what the
    scan gives, the old adders kept in the spec as the reference;
  - a bound on the comparisons a parse makes as its document doubles, counted, not timed.
- **s3-tests (PP-19):** none: a cost.
- **Exclusions and registry:** none.
- **Depends on:** nothing. **PR:** one. **Brief:** about 20 lines.

### Task S23: a Deny reads aws:SourceIp as an address

Found by the authorization sweep of 2026-11-21 (F-D), on main, and numbered here (PP-12).

- **Layer:** authz and policy. **Files:** S14's Deny-only context (`amazonKeys`,
  `internal/authz/env.go`), a Deny's evaluation of the IP operators
  (`internal/policy/condition.go`), their specs; `docs/differences.md`; the rgw-go line of the
  registry entry the registry sweep adds. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies
  (PP-14).
- **Why,** mechanism only:
  - `aws:SourceIp` is the host part of the request's remote address (`sourceIP` and `cgiVar`,
    `internal/authz/env.go:506-539`). net/http writes a link-local IPv6 peer's address with its
    zone, so the key's value carries the zone.
  - `ParseMaskedIP` is glibc's `inet_pton`, which takes no zone
    (`internal/policy/convert.go:209-240`), so that value does not convert, and a typed
    operator is false for a key whose value does not convert, the negated ones too
    (`typedNone`, `internal/policy/condition.go:1307-1318`).
  - So a Deny written as an allow-list, `NotIpAddress` over the ranges that may connect, does
    not hold for such a peer, and neither does a Deny over a range with `IpAddress`.
  - The same holds for any value of the key that is no address, such as the proxy chain a
    header `rgw_remote_addr_param` names can hold (the registry's "radosgw cuts X-Forwarded-For
    only for an rgw_remote_addr_param written in capitals").
  - radosgw, by reading and not run: its REMOTE_ADDR is the endpoint's address as written
    ([rgw_asio_client.cc:92](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_client.cc#L92), [rgw_asio_client.cc:92](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_client.cc#L92)),
    `as_network` hands it to `inet_pton` ([rgw_iam_policy.cc:1008-1068](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L1008-L1068),
    [rgw_iam_policy.cc:1012-1072](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L1012-L1072)), and `NotIpAddress` is false for a value
    that does not convert ([rgw_iam_policy.cc:974-992](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_iam_policy.cc#L974-L992),
    [rgw_iam_policy.cc:994-996](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_iam_policy.cc#L994-L996)).
  - Amazon's key is "the requester's IP address"
    ([reference_policies_condition-keys.md:1058](https://github.com/awsdocs/iam-user-guide/blob/e2b8637a825b9f292bb04af61277d067dfcc5676/doc_source/reference_policies_condition-keys.md?plain=1#L1058)):
    an address, never an address and a zone.
- **Decision:** in a Deny, `aws:SourceIp` is an address, or the Deny fails closed. Both rules
  act on a Deny alone, and an Allow keeps radosgw's evaluation, as S9 and S14 keep it. Reason:
  an allow-list Deny must not be passed by a peer whose address the gateway cannot read.
- **Behaviour:**
  1. A Deny reads the peer's address without its zone, through S14's Deny-only context
     (`policy.Env.SetForDeny`, `internal/policy/env.go:197-210`; `amazonKeys`,
     `internal/authz/env.go:258-296`). Radosgw's environment keeps the value as written.
  2. In a Deny, a negated IP operator holds over a value of `aws:SourceIp` that names no
     address, as S9 holds a negated operator over an absent key: a source the gateway cannot
     read is no better than none. A positive IP operator stays false over such a value, since
     no address is in any range; `docs/differences.md` says so.
  3. An Allow is unchanged: an `IpAddress` Allow grants nothing to a peer whose address carries
     a zone, as radosgw's.
- **Seams:** S14's `SetForDeny`, and the Deny reading of `Condition.EvalFor`.
- **Specs:** per release, in a Deny and in an Allow statement, through the evaluator with
  net/http's forms of the remote address:
  - a `NotIpAddress` Deny over two ranges: an IPv4 and an IPv6 peer inside a range served, one
    outside refused, a link-local peer with a zone refused, and served once its range is
    listed;
  - an `IpAddress` Deny over the link-local range: a peer with a zone refused;
  - `rgw_remote_addr_param` naming a header that holds two addresses, written in capitals and
    not: the `NotIpAddress` Deny refuses where the value is no address;
  - every Allow answer unchanged.
- **s3-tests (PP-19):** none: the suite cannot connect from a link-local address with a zone,
  nor set the gateway's `rgw_remote_addr_param`. The report says so.
- **Exclusions and registry:** `docs/differences.md`, "A Deny reads the condition keys Amazon S3
  sets", takes `aws:SourceIp`'s value and Behaviour 2; the rgw-go line of the registry entry.
- **Depends on:** nothing; S14 is on main. **PR:** one. **Brief:** about 35 lines.

### Task S24: PutObjectTagging's check carries the tags its body sets

Found by the authorization sweep of 2026-11-21 (R-1), on main, and numbered here (PP-12). It
overlaps public tracker [#82944](https://tracker.ceph.com/issues/82944), the same keys missing
from other checks.

- **Layer:** op, s3 and authz. **Files:** PutObjectTagging (`internal/op/objectattrs.go`,
  `internal/s3/objectattrs.go`), the environment (`opKeys`, `internal/authz/env.go`), their
  specs; `docs/differences.md`; the rgw-go line of the registry entry the registry sweep adds.
  **Cluster:** no.
- **Security boundary:** request authorization, and a client's document parsed before the
  request is authorized; adversarial review applies (PP-14).
- **Why:**
  - PutObjectTagging authorizes before it reads its body: `VerifyPermission`
    (`internal/op/objectattrs.go:204-207`) runs ahead of the `Params` call in `Execute`
    (`:216-221`), and `opKeys` adds no key for the tagging op (`internal/authz/env.go:616-625`).
    So its s3:PutObjectTagging check carries no `s3:RequestObjectTag` key.
  - radosgw is the same at both tags: `RGWPutObjTags::verify_permission` evaluates before
    `execute` reads the body and adds no key ([rgw_op.cc:1076-1097](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L1076-L1097),
    [rgw_op.cc:1275-1296](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L1275-L1296)), and the one line that sets the key is
    `RGWPutObj`'s, for its header ([rgw_op.cc:3970-3972](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L3970-L3972),
    [rgw_op.cc:4179-4181](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L4179-L4181)).
  - Amazon documents the key for this op: it is for "adding tags to objects using the
    PutObjectTagging and PutObject" requests, and its example grants s3:PutObjectTagging under
    `StringEquals` on `s3:RequestObjectTag/Project`
    ([tagging-and-policies.md:12](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/tagging-and-policies.md?plain=1#L12),
    [:72-89](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/tagging-and-policies.md?plain=1#L72-L89)).
  - Since S12 a policy conditioned on a request tag binds rgw-go's writes and not the tagging
    op, the op the key is documented for.
- **Decision:** the tagging op reads and parses its body before it authorizes, and its check
  carries the keys of the tags the body sets, in an Allow as in a Deny, as S13 gave the keys to
  CopyObject and CreateMultipartUpload. Reason: Amazon's own example is an Allow on this key
  for this op. PutObjectRetention is the pattern (`internal/op/objectretention.go:101-158`), as
  it is S17's.
- **Behaviour:**
  1. The op's `Init` reads the body under the bounds `Params` reads it with today, and parses
     it. A body that does not read or parse sets no key, and its error is kept and answered
     where `Params` answers it today, once the request is authorized.
  2. The check carries `s3:RequestObjectTag/<key>` for each tag of the body, for
     s3:PutObjectTagging and s3:PutObjectVersionTagging.
  3. A body that names a key twice is refused before the check, as S16 refuses a header that
     does, so that no check reads one value of the key.
  4. A missing object answers as today, after the check.
  5. A refused requester's body is now read, bounded, before the refusal, as DeleteObjects
     reads its body (`internal/op/deleteobjects.go:116-123`). The differences entry records it.
- **Seams:** the op carries the parsed set, or its error, from `Init` to `Execute`; the request
  carries the set for the environment, as it carries `List` and `Lock`
  (`internal/op/request.go:123-129`), which may also serve S19's seam for a write's tags.
- **Specs:** per release:
  - under a bucket Deny of s3:PutObjectTagging with `StringEquals` on a request tag: a body
    that sets it refused 403 with the object's tags unchanged, another value served;
  - under an Allow so conditioned as the only grant: served with the tag, refused without;
  - `Null` and a `ForAllValues` operator; a version named;
  - a malformed body: 403 for a refused requester, 400 MalformedXML for an allowed one; a body
    over the bound; a missing object; a body that repeats a key.
- **s3-tests (PP-19):** PutObjectTagging under a bucket Deny of s3:PutObjectTagging on a request
  tag is refused when its body sets the tag, and Amazon's Allow example grants it. radosgw
  answers both the other way, so the tests are `fails_on_rgw`. The task moves the pin with the
  known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry; the rgw-go line of the registry
  entry.
- **Depends on:** S16, which changes the same handler and decides the repeated key, and S19,
  which changes the same case of `opKeys`. **PR:** one. **Brief:** about 40 lines.

### Task S25: a statement matches an ARN's account by the comparison that grants less

Found by the authorization sweep of 2026-11-21 (R-3), on main, and numbered here (PP-12).

- **Layer:** policy. **Files:** `internal/policy/arn.go` (`ARN.Match`),
  `internal/policy/statement.go` (`Statement.Eval`), their specs; `docs/differences.md`; the
  rgw-go line of the registry entry the registry sweep adds. **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies
  (PP-14).
- **Why,** mechanism only:
  - `ARN.Match` compares a resource pattern's account with the candidate's without regard to
    ASCII case (`internal/policy/arn.go:336-348`), as radosgw's `ARN::match` does at both tags
    ([rgw_arn.cc:319-344](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_arn.cc#L319-L344), [rgw_arn.cc:319-344](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_arn.cc#L319-L344)).
  - An ARN's account is a tenant here, and a tenant's name is case-sensitive: letters, digits
    and underscores in either case (`validTenantName`, `internal/s3/request.go:420-428`).
  - A resource pattern with no account, or `*`, takes the tenant of the policy's owner at
    parse (`internal/policy/parse.go:410-417`).
  - So a statement's resource pattern matches a resource whose account differs from the
    pattern's only in case.
- **Decision:** each statement takes, of the exact and the case-insensitive comparison of the
  account, the one that grants less. Reason: tenant names are case-sensitive, so the exact
  comparison is the correct one, and where the wider match only refuses more it is kept, as
  S9 and S10 keep radosgw's reading in a Deny.
- **Behaviour:**
  1. An Allow's `Resource` and a Deny's `NotResource` compare the account exactly.
  2. A Deny's `Resource` and an Allow's `NotResource` keep the case-insensitive comparison.
  3. The region, the resource and the wildcards compare as today.
  4. Accounts that are equal, or that differ in more than case, give today's answers.
- **Seams:** `ARN.Match` takes the comparison, and `Statement.Eval`
  (`internal/policy/statement.go:92-110`) chooses it by the statement's effect and by which of
  `Resource` and `NotResource` it is matching (`matchesAny`, `:245-247`).
- **Specs:** per release, at the policy and through the evaluator, for an identity policy and
  a bucket policy:
  - a table over the four positions, an Allow's and a Deny's `Resource` and `NotResource`, with
    accounts equal, differing only in case, and different, with and without wildcards: each
    position gives the answer that grants less;
  - every existing ARN spec, unchanged.
- **s3-tests (PP-19):** the case needs an identity policy, which s3-tests sets through IAM
  under the `user_policy` marker, out until phase 3 (`hack/s3tests/excluded-markers.txt:49-55`).
  Its test is written with that marker and `fails_on_rgw`, and runs once phase 3 lifts it, as
  S17's third test does.
- **Exclusions and registry:** a `docs/differences.md` entry, mechanism only; the rgw-go line of
  the registry entry.
- **Depends on:** nothing. **PR:** one. **Brief:** about 25 lines.

### Task S26: a Deny reads s3:x-amz-copy-source in the form the copy reads its source by

Found by the authorization sweep of 2026-11-21 (O-1, an observation), on main, and numbered
here (PP-12). The brief asked whether it is a task or a note. It is a task: a Deny that another
spelling of one source escapes is the class S9 to S18 close, and this fix adds holds only, so
it does not wait on Amazon's exact form.

- **Layer:** authz. **Files:** the copy's keys and S14's Deny-only context
  (`internal/authz/env.go`), their specs; `docs/differences.md`; `docs/ceph-upstream-bugs.md`.
  **Cluster:** no.
- **Security boundary:** policy evaluation is authorization; adversarial review applies
  (PP-14).
- **Why,** mechanism only:
  - `s3:x-amz-copy-source` is the `x-amz-copy-source` header as sent, in both gateways
    (`copyKeys`, `internal/authz/env.go:693-700`; [rgw_op.cc:5477-5485](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_op.cc#L5477-L5485),
    [rgw_op.cc:6043-6051](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.cc#L6043-L6051)).
  - The copy reads its source through a decode: one leading `/` dropped, the name
    percent-decoded, and everything from the first `?` taken as arguments (`parseCopySource`,
    `internal/s3/copyobject.go:194-215`). So one source has many spellings, and a condition
    written for one does not read the others.
  - A Deny under a positive operator on the key therefore does not hold for another spelling
    of the source it names. A Deny under a negated operator holds for every spelling but the
    policy's, and an Allow grants the policy's spelling alone.
  - No pinned Amazon text says which form Amazon S3 compares. Its own example describes the
    header's value with a leading slash and writes the policy's value without one
    ([amazon-s3-policy-keys.md:146](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L146),
    [:171](https://github.com/awsdocs/amazon-s3-userguide/blob/1b918e0a0da664a139aad2ceb5d2ef10187c272f/doc_source/amazon-s3-policy-keys.md?plain=1#L171)),
    which reads as a normal form and not the header as sent.
- **Decision:** in a Deny the key is read as sent and in the forms the copy itself reads, and
  the condition holds where it holds on any of them. An Allow keeps the header as sent, which
  already fails closed. Reason: the request must not choose which of its own spellings a Deny
  judges.
- **Behaviour:**
  1. A Deny's condition on the key holds where it holds on the header as sent, on the decoded
     `<bucket>/<key>` followed by the header's `?` and what follows it, or on the decoded
     `<bucket>/<key>` alone, through S14's second readings (`policy.Env.SetForDenyOr`,
     `internal/policy/env.go:224-230`). The decode is `parseCopySource`'s own, so the forms a
     Deny reads are the forms the copy acts on.
  2. A negated Deny, the form Amazon's example uses, still holds for a spelling other than the
     policy's, as today and as on radosgw: the reading as sent holds it. The differences entry
     records that a client must spell the source as the policy does to pass it.
  3. An Allow is unchanged.
  4. UploadPartCopy carries no such key on either gateway and is not changed here.
- **Seams:** S14's `SetForDenyOr`, set where `copyKeys` sets the key; the decode is shared with
  the handler, not copied.
- **Specs:** per release, in a Deny and in an Allow statement:
  - a Deny with `StringLike` and with `StringEquals` on the key: the source as the policy
    spells it, with a leading slash, percent-encoded in its name and in its separator, and with
    a `?` suffix that names a version and one that names nothing, each refused; another source
    served;
  - the negated Deny and the Allow as the only grant: today's answers for every spelling;
  - a tenant-qualified source.
- **s3-tests (PP-19):** under a bucket Deny of s3:PutObject with `StringLike` on the key, a copy
  whose header spells the denied source with a leading slash, and one that percent-encodes it,
  are refused. radosgw serves them, so the test is `fails_on_rgw`. The task moves the pin with
  the known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry beside "A Deny reads the condition
  keys Amazon S3 sets"; a `docs/ceph-upstream-bugs.md` entry for radosgw's side, mechanism only,
  possibly security-relevant and sent to rgw-bug-reproduction (PP-4), unless the registry sweep
  has added it.
- **Depends on:** nothing; S14 is on main. **PR:** one. **Brief:** about 35 lines.

### Task S27: UploadPart and UploadPartCopy take no ACL header

Found by S15's work (its report's concern 2), and numbered here before S15 merged (PP-12).

- **Layer:** s3, op and authz. **Files:** the part's parameters and checks
  (`internal/s3/multipart.go`, `internal/op/uploadpart.go`), the part's keys
  (`putObjectKeys`, `internal/authz/env.go`), their specs; `docs/differences.md`;
  `docs/ceph-upstream-bugs.md`. **Cluster:** no.
- **Security boundary:** request authorization, since the keys a part's check carries change;
  adversarial review applies (PP-14).
- **Why:**
  - Amazon S3's UploadPart and UploadPartCopy have no ACL or grant parameter
    ([service-2.json:14198](https://github.com/boto/botocore/blob/4c688659d4a2ccc0edcdc3357107a534a52e478d/botocore/data/s3/2006-03-01/service-2.json#L14198),
    [:13960](https://github.com/boto/botocore/blob/4c688659d4a2ccc0edcdc3357107a534a52e478d/botocore/data/s3/2006-03-01/service-2.json#L13960)),
    and an AWS CLI maintainer's test of 2015-12-10 got 400 InvalidArgument, "The specified
    header is not valid in this context", for an UploadPart sent with `x-amz-acl`
    ([aws/aws-cli#1674](https://github.com/aws/aws-cli/issues/1674#issuecomment-163717329)).
    That report is first-hand and old; the same thread later says, unconfirmed, that Amazon
    changed something on its side in 2018.
  - Both gateways take the header. rgw-go builds an ACL from a part's headers and stores it on
    the part's head (`uploadPartChecks`, `internal/s3/multipart.go:299-304`;
    `internal/op/uploadpart.go:281-291`, `:308`), as radosgw's `RGWPutObj` does for a part, and
    the part's s3:PutObject check carries `s3:x-amz-acl` from the part's own header
    (`putObjectKeys`, `internal/authz/env.go:706-710`). The completed object takes the upload's
    ACL, not a part's.
  - S13 pins that a part must repeat the canned ACL to pass a Deny for its absence, and S15
    decides what a completion's check reads.
- **Behaviour:**
  1. **Establish Amazon's answer today first,** from the sources S15's step 1 names, for a part
     sent with `x-amz-acl`, for one sent with a grant header, and for one sent with neither
     under a Deny for the ACL's absence. The task records each source read. If one shows that
     Amazon takes the header today, the task stops for the owner's ruling.
  2. Otherwise UploadPart and UploadPartCopy answer 400 InvalidArgument to `x-amz-acl` and to
     each `x-amz-grant-*` header. The refusal reads the request's own headers alone, so it is
     made where the handler makes its other header refusals, before authorization (B16).
  3. A part's head takes the ACL a part sent without a header takes today, and the refusal of
     a public ACL under the bucket's block goes with the header.
  4. The part's s3:PutObject check carries no ACL key of the part's own. What it carries
     follows S15's ruling: under its outcome (a) none, so a Deny for the ACL's absence refuses
     every part as it refuses every completion; under (b) the upload's recorded keys, so the
     parts of an upload created with the ACL pass. S13's spec and its differences sentence
     change with it.
- **Seams:** under S15's outcome (b), the part's check reads the record S15 gives the upload;
  under (a), none.
- **Specs:** per release, for UploadPart and UploadPartCopy:
  - a part sent with `x-amz-acl` and with each grant header: 400 InvalidArgument and no part
    stored, for an allowed and for a refused requester;
  - a part sent with neither: stored, its head's ACL as today;
  - under the Deny for the ACL's absence and under the Allow form as the only grant: the
    answers S15's ruling gives;
  - CreateMultipartUpload's ACL and the completed object's, unchanged.
- **s3-tests (PP-19):** an UploadPart sent with `x-amz-acl` answers 400 InvalidArgument.
  radosgw serves it, so the test is `fails_on_rgw`, and step 1 decides whether it is also
  `fails_on_aws`. The task moves the pin with the known-differences lines.
- **Exclusions and registry:** a `docs/differences.md` entry; a `docs/ceph-upstream-bugs.md`
  entry for radosgw taking an ACL header on a part, S3-correctness.
- **Depends on:** S15, for its ruling and the record of an upload's keys. **PR:** one.
  **Brief:** about 40 lines.

## Order

1. **Before any phase 2 task:** unit F, the floor bump, then the deferred benchmark run on v19.2.7 and v20.2.4 (spec §9), then S1, S2 and S3 in parallel, S4 once S1 lands, S5 at once, S6 once V2 lands, S7 once V10, BC-17, BC-23, BC-10 and BC-28a land, S8 and S11 at once, S9 and S10 once L7 lands, S12 once S8 lands, S13 once S12 lands, S14 once S9 lands, S15 and S16 once S13 lands, S17 and S18 once S14 lands, S19 once WP-31 lands, S20, S21, S22, S23, S25 and S26 at once, S24 once S16 and S19 land, and S27 once S15 lands.
2. **Then the units in parallel**, each in its own file's waves. V's foundation (V0 to V3b) comes first among the units, because L and C consume its OLH seams. B and WP start with S2, and the T and SM units start at once. Within the waves:
   - The cluster tasks of every unit run one at a time.
   - The recording tasks, which append to the parity ledgers, land one at a time through the controller.
   - The tasks numbered on 2026-11-28 from the sweeps of 2026-11-21 and 2026-11-28 wait for no wave. F21, F24, F26, F27, F28, F29, F30, F32, V32, C16, WP-33, WP-35, WP-36 and WP-37 start at once, and so does the presigned route of F22. F22's header route waits for the owner's ruling on FD-10 (unit F), F23 for that ruling and F22, F25 for F22, F31 for F30, and WP-34 for WP-33. The write side added to V28 follows V28's delete side. C16 merges apart from V27 and V28, which change the same file.
   - V33, numbered on 2026-11-28 from V25's review, starts once V25 lands.
3. **The hot-file rule** (PP-5, settled after the whole-plan review). Where two tasks only add a field, a helper or one call line to a shared file, no order is fixed: whichever lands first, the later rebases. A fixed order stays only where two tasks change the same function body: WP-6, V11 and BC-26 on the write-path op files, and V7b and WP-9 on `mp_complete.go`.
4. **The cross-unit dependencies,** stated in each unit's Seams:
   - L and C consume V's versioned delete and delete-marker writers.
   - C's in-cluster transition (C11) consumes WP-11's raw copy; its placement lookup is phase 1's.
   - V13a (MFA) consumes WP-16's otp class client, and V13b WP-17's emulator.
   - BC-26 changes write-path ops after WP's tasks on the same files, never in parallel with them.
5. **The phase 2 gate** (T unit) runs last.

## The phase 2 gate

Spec §9: "Every phase gate includes writing with one gateway and reading with the other on a shared zone, s3-tests parity with radosgw for the phase's ops, and a benchmark run." For phase 2 that is:
- s3-tests parity on both releases for the phase 1 set plus phase 2's: the deselect-phase2 lines lifted, the markers phase 2 serves, the include list, and P1's time-driven tests;
- the go-ceph rgw/admin suite with its phase 2 deselects lifted;
- the Rook suite;
- each unit's cross-gateway cases;
- zero open findings;
- a benchmark run of the phase 2 paths.

## Deferred to phase 4

Work a phase 2 task leaves to phase 4. Phase 4's planning reads this list and turns each item
into a task.

1. **A CompleteMultipartUpload of an SSE-C upload takes its SSE-C names (WP-32).** WP-32
   answers 501 to a completion that sends any encryption name with a value, the SSE-C names
   included, so over the S3 API an SSE-C upload with a checksum type cannot be completed until
   phase 4, where radosgw and Amazon S3 complete it for the holder of its key. Phase 4, which
   serves SSE-C:
   - lets the SSE-C names through on the completion of an SSE-C upload. The SSE-S3 and SSE-KMS
     names keep their 501 (BC-26 item 5). On an upload without a crypt mode the SSE-C names
     keep it too, unless phase 4 shows that letting them through cannot let a valued SSE-C
     algorithm satisfy Tentacle's `Null` condition on
     `s3:x-amz-server-side-encryption-customer-algorithm` and complete an upload stored in the
     clear;
   - restores the WP-26 handler specs that WP-32 deleted or turned into 501 pins, as they stand
     in `internal/s3/completemultipart_crypt_test.go` at 5865b07:
     - "answers a Tentacle SSE-C upload's combined checksum to the holder of its key, as for
       any upload";
     - "echoes the key MD5 as the request sent it, white space the decode skips included";
     - "sends the key's headers on an error after the decision passed, as radosgw does", which
       WP-32 moved to an SSE-KMS upload; the SSE-C case returns beside it;
     - "answers the key holder's retry with the combined checksum and no SSE-C pair, as no
       radosgw echoes one there, on Tentacle";
     - "reads the last of a repeated SSE-C header, as radosgw's environment keeps it";
     - "completes an upload without a crypt mode whatever SSE-C headers the request sends,
       echoing none, on both releases", only if the first bullet lets those names through;
     - the SSE-C entries of the table "answers an SSE-C upload's completion as Amazon S3 does,
       echoing the key's headers on Tentacle" and of the retry table's "SSE-C, its own id,
       another key", back to the decision's own answers (200 with the echo, 400
       InvalidArgument, 400 InvalidRequest, 400 InvalidEncryptionAlgorithmError), and, in
       `internal/s3/emptyencryption_test.go`, the KMS key id sent empty beside the right SSE-C
       key;
   - accounts for the s3-tests that send SSE-C on a completion and are out under the
     `encryption` marker until then, among them test_encryption_sse_c_multipart_upload,
     test_encryption_sse_c_unaligned_multipart_upload and
     test_encryption_sse_c_multipart_bad_download.
