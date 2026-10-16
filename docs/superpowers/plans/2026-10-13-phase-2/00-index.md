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
| `00-index.md` | this index, and the shared tasks S1 to S6 | 6 |
| `V-versioning.md` | the OLH protocol, versioned ops, ListObjectVersions, MFA delete | V0-V15c |
| `L-object-lock.md` | object lock: configuration, retention, legal hold, enforcement | L1-L5b |
| `C-lifecycle.md` | lifecycle configuration and the worker | C1-C13 |
| `B-bucket-config.md` | CORS, website, encryption configuration, ownership controls, public access block, policy status, POST object | BC-2 to BC-35 |
| `WP-write-path.md` | checksums, compression and placement on writes, `?layout`, admin otp | WP-1 to WP-21, WP-24 |
| `T-gate-tooling.md` | the s3-tests selection in Go, the include list, the harness, the phase 2 gate | T-tasks, with WP-22 and WP-23 |
| `F-floor-19.2.7.md` | the floor bump to Squid v19.2.7: phase 1 maintenance that lands before phase 2 | F1-F20 |
| `CL-citation-links.md` | Ceph citations as clickable links, in the docs and in Go comments | CL1-CL6 |

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
- **The next s3-tests pin bump** adds the known-differences lines that the tests landed since the pin need: F13's `test_sigv4_presigned_put_xgoog_meta_unsigned`, as F18 revises it, in both known-differences files (rgw-go stores no metadata from the unsigned header, radosgw stores it as user metadata), and F15's `test_sigv2_signed_copy_source_moved_to_xgoog`, in both (rgw-go refuses the relabelled copy source, radosgw stores a plain PutObject, as F15's runs on both releases showed).
- **Go conventions:** go-conventions (Ginkgo v2 and Gomega specs, errors wrapped, slog). Every build, test and lint passes `ceph_preview`; cgo builds use the project CLAUDE.md's `CGO_CFLAGS` and `CGO_LDFLAGS`. No task ids in code.
- **Clusters:** only disposable rooket clusters (`make cluster-up RELEASE=squid|tentacle`), reached through `ROOKET_NAME=rgw-go-<release> rooket k`; never the ambient kubectl. A task marked "Cluster: yes" runs one at a time.
- **rgw-rs:** every material change to `docs/exclusions.md` is announced to the rgw-rs session.
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
| D8 | `docs/exclusions.md`'s two `bi_*` sentences are amended: `bi_get` is in scope for one version's index entry, on the versioned delete and on D2's 404. That is one read for a named instance, and up to three for `null` (the plain key, then the null delete marker's key, then the null data key, in the class's `read_key_entry` order). `bi_put`, `bi_list` and the repair paths stay out. rgw-rs is told. | Q9. |
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

- **Layer:** acl and authz (`internal/acl`, `internal/authz`). **Files:** modify `internal/acl/eval.go` and the authz evaluator's ACL checks, their specs, and `docs/exclusions.md`. **Cluster:** for one spec, if the rooket clusters' radosgw serves Swift.
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

## Order

1. **Before any phase 2 task:** unit F, the floor bump, then the deferred benchmark run on v19.2.7 and v20.2.4 (spec §9), then S1, S2 and S3 in parallel, S4 once S1 lands, S5 at once, and S6 once V2 lands.
2. **Then the units in parallel**, each in its own file's waves. V's foundation (V0 to V3b) comes first among the units, because L and C consume its OLH seams. B and WP start with S2, and the T unit starts at once. Within the waves:
   - The cluster tasks of every unit run one at a time.
   - The recording tasks, which append to the parity ledgers, land one at a time through the controller.
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
