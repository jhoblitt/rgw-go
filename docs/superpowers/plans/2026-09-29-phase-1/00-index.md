# Phase 1 plan set: index

Nine unit plans implement phase 1 of the design spec
(`docs/superpowers/specs/2026-09-25-rgw-go-design.md`, §9): 105 tasks. Each
plan argues from the spec; where a plan and the spec disagree, the spec
governs unless this index records an amendment. Ceph floor: every daemon at
19.2.6 or later on Squid, 20.2.4 or later on Tentacle (spec §3).

Status: the nine plans are complete, and the owner's decisions of
2026-09-29 are applied in the plans that "Owner's decisions" names for
each; nothing is open. The background sections at the end record why
phase 1 is split and ordered as it is.

For executors: a task's code blocks are sketches, not text to transcribe.
The behaviour its steps and specs describe, the interfaces at the seams
between tasks and the radosgw citations bind; an implementer who finds a
block that does not compile or contradicts another task follows the
behaviour and records the ruling in the task's report. Comments in the
code give radosgw's reasons, never plan, task, unit or decision IDs; the
task reviewer checks for them. A task that introduces a behaviour differing
from radosgw records it in `docs/exclusions.md` in the same PR, with the
text its steps give, and the task reviewer checks that the entry exists
and matches what the code does (the owner's standing rule, decision 9b).
Each such change is reported so the rgw-rs session is told.

| Unit | Plan | Tasks | Scope |
|---|---|---|---|
| G | `G-gateway-core.md` | 9 | interface freeze (op, policy.Action, cephconf, frontend, memstore, fakes), seam prerequisites, s3 parsing and dispatch, handler, frontend, cephconf startup, metrics, driver skeleton, `cli serve` |
| T | `T-gates-benchmarks.md` | 15 | seam microbenchmark and cgo answer, s3-tests and admin-suite harnesses and radosgw baselines, derived image, Rook workflow, parity runs, gateway comparison |
| A | `A-auth.md` | 10 | SigV4 header, presigned, chunked and trailer payloads; SigV2; anonymous; identity |
| Z | `Z-authorization.md` | 12 | ACLs, IAM policy evaluation incl. stored user policies, tags, the op.Authorizer |
| M | `M-metadata-plane.md` | 13 | zone and placement, control watch and cache, users, buckets, listing, stats and quota, usage log, bucket subresources |
| R | `R-object-read.md` | 8 | manifest iteration, compression codecs, head op with prefetch, GET/HEAD, GetObjectAttributes, read gate |
| N | `N-admin-api.md` | 14 | the admin REST API, metadata store, usage read and bounded trim, accounts, realm/period getters, the bypass-gc purge |
| W | `W-object-write.md` | 13 | PUT, DELETE, DeleteObjects, COPY, set-attrs, completion manager, GC worker, write gate |
| P | `P-multipart.md` | 11 | cls_lock package, multipart meta and parts, the seven multipart ops, multipart gate |

## Execution order

Waves, from the dependency order ("Dependency order" below) with the
dependencies the plans added. A unit starts when every unit it depends on has
merged the tasks it consumes.

| Wave | Units | Depends on |
|---|---|---|
| 0 | G (Task 1, the interface freeze, merges first); T Tasks 1-8 | phase 0 |
| 1 | A, Z, M | G Task 1 |
| 2 | R | M (driver Store, fakerados, placement) |
| 2 | N Tasks 1-12 | M, A (identity caps, account store) |
| 3 | W | R (`PrefetchObject`, `ObjectState.Head`, `attrs_read.go`, `ReadObject` for copy), M (`AdjustStats`, shard helpers), A (body-EOF rule) |
| 4 | P | W (writer machinery, `atomic=false` head writes), R, M (`ListObjects` with `NameFilter`) |
| 4 | N Task 14, then N Task 13 (N's gate) | W Tasks 4-6 and 9 (`gcChain`, `rawTag`, `newIndexOp`/`prepare`, `deleteObjIndex`, the pools, `gcMaxConcurrentIO`), R Task 3 (`readHead`, `headRef`), P Task 7 (`abortMultiparts`) |
| 5 | T Tasks 9-15 | per T's own task index |

Three tasks edit code that another unit's task creates, and wait for that
task: M Task 7 fills G's `ListBuckets.Complete` (`internal/op/listbuckets.go`,
G Task 4); W Task 6 extends M Task 8's `checkDiskState` and uses R Task 1's
`Manifest.Seek`; P Task 7 edits M Task 7's `DeleteBucket` and pages M Task 8's
`ListObjects`. N Task 14, the bypass-gc purge, edits no other unit's file but
calls W's, R's and P's driver internals, so it waits for W Tasks 4-6 and 9, R
Task 3 and P Task 7, and N's gate, Task 13, runs after it; N's other twelve
tasks keep wave 2.

No unit needs a go-ceph fork PR or a pin bump: W's `OpFlagFullTry` and
alignment calls are already bound at the pinned `cbf97f85fcf5`, and N's
`FSID` and `ListObjectsFrom` are radosclient wrappers over upstream calls.

## Contract amendments

G's "Frozen interface contract" appendix is the base. Later plans extend it
additively; nothing is renamed or removed. These amendments are normative:
a task that consumes one of them depends on the task that produces it.

| Addition | Produced by | Why |
|---|---|---|
| `meta.ObjVersion` (only `version.ObjVersion` exists on main) | M Task 3 (M-D9), unless G Task 1 adds it | G's contract names it as existing |
| Bucket- and object-scope ACL routes share `get_acls`/`put_acls`: one registration with a scope switch; `s3.objectGetACLs`/`s3.objectPutACLs` variables set by R and W | M Task 11 (M-D8) | a real collision under per-unit handler maps |
| `op.LogUsage`; `op.UsageEntry.Payer` | M Tasks 7, 10 | radosgw keys requester-pays usage by payer |
| `op.ListObjectsParams.AllowUnordered` | M Task 8 | radosgw's allow-unordered listing |
| `op.ListObjectsParams.NameFilter` | P Task 5 (P-D6) | the multipart namespace's access_list_filter |
| `StatsStore.AdjustStats` | M Task 9 (for W-D7) | stats deltas from the write path |
| `op.AuthResult.ContentLength` | A Task 1 | aws-chunked decoded length |
| `op.AccountStore` (A: `GetAccount`, `AccountName`; N widens: by-name/email readers, put/remove, users index), `op.AccountRecord`, `Env.Accounts` | A Task 1, N Task 1 (N-D5) | one store satisfying A's and Z's shapes; `AccountName` is A's so `authz.UserResolver{Users: r.Env.Users, Accounts: r.Env.Accounts}` compiles in wave 1 |
| Body verification reported on the EOF read (D-A2); consumers read to EOF before completing | A Tasks 5-7 | radosgw's completer semantics |
| `ObjectStore.PrefetchObject`, `ObjectState.Head` | R Task 3 | the head op with first-stripe prefetch; `StatObject` stays data-free |
| `internal/meta/attrs_read.go` (read-side attr names) | R Task 1 | W reuses them |
| `op.UsageReader`, `op.BucketAdminStore`, `op.RealmStore`; `Env.UsageReader`, `Env.BucketAdmin`, `Env.Realms`, `Env.ClusterID`; `policy.ActionNone`; meta cap/perm parsers | N Task 1 | the admin API |
| `/admin` as a second handler beside G's S3 handler | N Task 3 (N-D1) | G's dispatch has no admin route |
| `Cluster.FSID()`, `Pool.ListObjectsFrom` (placement-hash tokens) | N Task 2 | go-ceph's `Iter.Token()` is the next-batch cursor |
| `PutParams.Tag`, `PutParams.ContentMD5`; `DeleteParams.UnmodifiedSince/IfMatchSize/IfMatchLastModified`; `CopyParams.Tag`; `meta.Manifest.SetHead`; an ENAMETOOLONG sentinel | W Tasks 5, 6, 8, 10 | the write path |
| `op.Upload.WriteTag`, `IfMatch`, `IfNoneMatch` | P Task 3 | request-scoped values Complete and CopyPart need |
| `op.Authorizer.VerifyBucketIn(ctx, r, a, perm, bucket, key)` and `VerifyObjectIn(ctx, r, a, perm, bucket, obj)`; `op.VerifyBucketPermissionIn`, `op.VerifyObjectPermissionIn`; `OwnerOnly` implements them | G Task 1; Z Task 10 implements them on `Evaluator`; W Task 10 and P Task 8 consume | CopyObject's and UploadPartCopy's source checks and DeleteObjects' per-key check keep the public-access block and requester-pays on the request's bucket, as radosgw does; no caller copies the request with its bucket swapped |
| `op.URLEncode(s string, encodeSlash bool)` (radosgw's `url_encode` over `char_needs_url_encoding`) | G Task 1; M Task 8 and P Task 9 consume | one encoder for the transaction id and the `encoding-type=url` listings |
| `s3.userResolver(r) authz.UserResolver` (`{Users: r.Env.Users, Accounts: r.Env.Accounts}`) | M Task 7; W Task 11 and P Task 9 consume | the one struct literal for Z's resolver |
| `s3.Dispatch(r, rel denc.Release)` (was `Dispatch(r)`) | G Task 3 | release-gated dispatch rows; G-internal, no other consumer |
| `op.PayloadForms` (`PayloadSigned`, `PayloadChunked`); `s3.Route.Payloads`; `s3.Authenticator.Authenticate(ctx, req, payloads op.PayloadForms)` and `s3.AuthenticatorFunc` (were two-argument) | G Tasks 1, 3, 4; A Task 9 implements it; N's admin handler passes its own routes' forms (`op.PayloadSigned` for the metadata put, none for every other admin op) | radosgw refuses a signed payload form by the dispatched op type inside authentication (owner decision 8, A's D-A1); `auth` must not import `s3`, so the forms travel as an `op` type |
| `s3.sinkOf(w)`, the `op.Sink` adapter (the handler's writer is not a sink); `s3.startChunkedXML`/`writeChunkedXML`; `op.ListBuckets.Begin`/`Page` (were the `Buckets`/`NextMarker` results) | G Task 4; R Task 7 consumes `sinkOf`, M Task 8 and P Task 9 `writeChunkedXML`, W Task 11 `startChunkedXML` | `http.ResponseWriter.WriteHeader(int)` and `op.Sink.WriteHeader(int, http.Header)` cannot share a type; radosgw frames its listings chunked and streams ListBuckets page by page (owner decision 12d, applied to every chunked response) |
| `internal/xmltext` (`Escape`, ceph's `xml_stream_escaper`; `Text`, the element-text type whose `MarshalXML` writes through it), a leaf importing only the standard library; every string field of an S3 XML document is an `xmltext.Text` and hand-written markup escapes through `Escape` | G Task 4; M Tasks 7-8, R Task 7, W Task 11, P Task 9 and Z Tasks 7-8 consume; N Task 3's `formatter` uses `Escape` instead of its own | radosgw's `XMLFormatter` escapes text with `&quot;`, `&apos;` and `&#x01;`-style references and copies every byte from 0x80 up, where `encoding/xml` writes `&#34;` and `&#39;` and replaces control bytes and invalid UTF-8 with U+FFFD; one escaper keeps every document alike |
| `s3.SetContentLength(h, n uint64)` (radosgw's `dump_content_length`: the length with `Accept-Ranges: bytes`); G's response writer no longer adds `Accept-Ranges` to every `Content-Length` | G Task 4; `WriteError`, R Task 7 (GET and HEAD), W Task 11 and P Task 9 (PUT of an object or part without a copy source), N Task 3 (admin errors and the realm and period getters) call it | radosgw sends `Accept-Ranges` only where `dump_content_length` runs: every error document and a success whose op names its length; every other success carries the length its frontend adds, without it |
| `github.com/pierrec/lz4/v4` (R Task 2, decision D3) and `github.com/cespare/xxhash/v2` (W-D4) | R Task 2, W Task 5 | two third-party dependencies; spec §4's "Third-party dependencies, complete" list is amended on main to name both (owner decision 13) |

## Owner's decisions (2026-09-29)

The owner settled the fourteen questions the plans left open. Each entry
gives the outcome and where it is applied; a behaviour that still differs
from radosgw is recorded in `docs/exclusions.md` by the task named.

1. **conditional_write: implemented.** If-Match and If-None-Match on PUT
   are served as each release's radosgw serves them (W-D3; W Task 10's
   pass-throughs, W Task 13's patterns). Settles 14.
2. **W's cancel version: radosgw's `-1:0`.** The cancel sends what
   radosgw sends and meets tracker #80894's stale-epoch defect as radosgw
   does until a release carries the class fix (ceph/ceph#72097). Applied in
   W-D8, W's Review Focus 1 and Task 4 and 5 specs, and the rgw-go line of
   the registry's complete_op entry.
3. **aws-chunked: a 1 KiB trailer bound and strict hex chunk sizes, both
   filed upstream.** A trailer section longer than 1024 bytes is 409
   LimitExceeded, where radosgw truncates silently and fails a signed
   section longer than 257 bytes with 403 (tracker #81122); a chunk size is
   one to sixteen hex digits, where radosgw's `strtoull` wraps `-1` and
   seventeen digits and stores a corrupted unsigned multi-chunk upload
   (tracker #81123). Applied in A's D-A7: A Task 6 implements both and
   records them in `docs/exclusions.md`, A Task 10 adds rgw-go's handling
   to the two registry entries main carries.
4. **Cross-pool copy of a compressed object: a raw copy, like radosgw.**
   The copy streams the stored stripes and keeps `user.rgw.compression`,
   as `copy_obj_data` does, through a raw stripe read R's driver exposes;
   no longer a difference. Applied in W-D13 and R's driver.
5. **partNumber GET/HEAD ETag: radosgw's, as R Task 8's cluster
   comparison measures it** (the part head's own ETag by code reading).
   radosgw's behaviour wins over AWS and s3-tests, so a radosgw failure of
   `test_multipart_get_part` is parity. Applied in R-D17.
6. **IAM group policies: fail closed until phase 3.** Z's authorizer
   answers AccessDenied to any request whose identity lists IAM groups,
   logging the user once; `op.Run`'s admin override still applies. Applied
   in D-Z2; Z Task 10 implements it and records it in `docs/exclusions.md`.
7. **Checksums: the spec's phasing.** `x-amz-checksum-*` headers and
   trailers stay ignored in phase 1 on both releases; Tentacle-level
   checksums are phase 2. Applied in P-D5 and A's trailer handling (D7); P
   Task 3 records the Tentacle gap, no validation and no `user.rgw.cksum`,
   for single PUT and multipart in `docs/exclusions.md`.
8. **Per-op payload whitelist: reproduced.** radosgw accepts a signed
   single-chunk payload for 33 op types at v19.2.6 and 37 at v20.2.4, and
   an aws-chunked one for `RGW_OP_PUT_OBJ` only, answering 501
   NotImplemented otherwise, from inside authentication. G's dispatch table
   carries each route's forms per release, `s3.Authenticator` takes the
   dispatched route's forms, and A's verifier refuses at radosgw's point in
   the flow. Applied in G Tasks 1, 3 and 4 and A's D-A1 (Tasks 5 and 9);
   not a difference.
9. **The admin API.** (a) XML in phase 1: every admin response renders
   through a ceph-style formatter with JSON and XML implementations,
   selected by `format=` and `Accept` as radosgw selects (N-D2). (b) Usage
   trim stays bounded (N-D4), a difference N records in
   `docs/exclusions.md`; with it the owner set the standing rule that every
   behaviour differing from radosgw is recorded there by the task that
   introduces it ("For executors" above). (c) `bypass-gc` is honoured as
   `RadosBucket::remove_bypass_gc` honours it (N-D9).
10. **Zone bootstrap: read-only.** rgw-go never creates a zonegroup or zone
    and never writes the master-zone fix-up; a missing object is a startup
    error naming it. M-D1's citation now covers `rgw::SiteConfig::load` on
    both releases and Squid's zone service, and M Task 13's
    `docs/exclusions.md` entry gives Rook's reason: explicit realm,
    zonegroup and zone names, and a zonegroup and zone created with
    `--master` before any gateway starts. M Task 13 records decision D4's
    no-pool difference beside it.
11. **GetObjectAttributes on Squid: served on both releases.** R-D1 stands
    (spec §3's Tentacle feature level): G's `attributes` row is registered
    for both releases, R Task 7 records the Squid difference in
    `docs/exclusions.md`, and T Task 12's `known-differences-squid.txt`
    carries `test_get_object_attributes`.
12. **The remaining differences.** (a) Swift DLO/SLO objects answer 501
    over S3 where radosgw composes them, documented in
    `docs/exclusions.md`'s Swift section (R-D7). (b) `?torrent` keeps 404
    when absent and 501 when present, with an exclusions entry from the R
    task that registers it (R-D8). (c) ACL documents escape the names
    radosgw writes raw, documented by Z Task 7 (D-Z7). (d) Every phase 1
    response radosgw sends with chunked transfer encoding (`end_header(...,
    CHUNKED_TRANSFER_ENCODING)`) is framed like radosgw's: DeleteObjects
    (each result flushed as its key completes), CopyObject (the body at the
    end), ListBuckets (each page of `rgw_list_buckets_max_chunk` buckets
    flushed as it is read), and ListObjects v1 and v2, ListParts and
    ListMultipartUploads (the headers first, the document in one flush).
    Each carries `Transfer-Encoding: chunked` and neither `Content-Length`
    nor `Accept-Ranges`; a HEAD of a listing gets radosgw's bare
    `Content-Length: 0`. None is a difference (W Tasks 10 and 11, which also
    render DeleteObjects' DeleteMarker fields; G Task 4's `startChunkedXML`,
    used by G's ListBuckets, M Task 8 and P Task 9). A DeleteObjects that
    fails before its first key answers as radosgw's `send_status` does, the
    status line alone (W Tasks 10 and 11). GetUsage, radosgw's other chunked
    response, is phase 3.
13. **Dependencies: both accepted.** `cespare/xxhash/v2` (W-D4) and
    `pierrec/lz4/v4` (D3, R Task 2) are named in spec §4's list, amended on
    main; no fallback applies.
14. **T's `conditional_write` marker: settled by 1.** Nothing is added to
    T Task 4's `EXCLUDED_MARKERS`.

## Background

The decomposition that split phase 1 into the nine units, kept as the record
of why the work is cut and ordered as it is. Its claims about radosgw, Rook
and go-ceph were checked against the checkouts of 2026-09-25 (ceph main at
7ed73efc1be, rook at dc7829268, go-ceph at f18cd2a), and its file:line
references are to those trees; the plans re-verify every radosgw claim at
v19.2.6 and v20.2.4, and where a plan says otherwise, the plan governs.

### Why nine units

A spec covering several independent subsystems becomes one plan per
subsystem, each producing working, testable software. §9's phase 1 covers
the gateway shell (config, frontend, dispatch), authentication,
authorization, the metadata plane, the object read path, the object write
path, multipart and the admin API, and names the gates that judge them; each
is one unit, nine in all, one over the four to eight plans aimed for. The two
merge candidates were A+Z (identity and authorization, both pure logic gated
by s3-tests groups) and R+W (the object read and write paths, both
`internal/driver`); they stay apart because R is verifiable against the
phase-0 oracle corpus before a single write exists, and A and Z are the two
units most safely handed to parallel workers.

A unit is one plan that ends in an independently runnable gate: unit specs on
fakes, the oracle matrix on a disposable cluster (write with radosgw, read
with rgw-go, and the reverse), an s3-tests group set, the go-ceph rgw/admin
suite, a benchmark, or a Rook sub-test. Sizes below are order-of-magnitude
estimates of Go lines excluding tests, from the C++ they transcribe; each
plan's "File structure" section replaces the package list the decomposition
drafted.

### The nine units

**G, gateway core: config, frontend, dispatch, op contract** (~3k). Goal: the
binary starts under Rook's `radosgw` argv, connects through librados, serves
HTTP and TLS from the beast spec, stamps transaction ids, parses Host, path
and query once, dispatches on radosgw's method, scope and subresource
precedence to an op, and renders radosgw's error documents — with every op
still a stub except the pipeline-baseline handlers. §9: TLS through the beast
spec; the pipeline baseline (no-op and one-round-trip handlers, which G fixes
as request shapes, not flags); §5 in full (radosgw argv, early arguments, mon
config store, `rgw_enable_apis`, swift ignored with one log line, own flags);
§6's transaction id, per-request deadlines, drain. Gates: unit specs
(dispatch table against radosgw's precedence, beast spec parsing, frontend
in-process with TLS, argv rewrite); the pipeline baseline; Rook's startup and
readiness probe (`rgw-probe.sh`: any 2xx-3xx on the endpoint root) once the
image exists.

**A, authentication, `internal/auth`** (~2.5k). Goal: every request resolves
to an `op.Identity` or a radosgw-shaped error: SigV4 header, query and
presigned; streaming chunked payloads with and without trailers, verified as
bytes stream; SigV2; anonymous; radosgw's quirks, above all treating a
missing `x-amz-content-sha256` as unsigned payload (`docs/exclusions.md`,
required by go-ceph's admin client). §9: "SigV4 header, query and presigned
with chunked and trailer payloads, SigV2, anonymous". Gates: unit vectors
(AWS SigV4 test-suite vectors, radosgw quirk cases, chunk-signature
vectors); s3-tests auth groups; the go-ceph rgw/admin suite (it signs with
the unsigned-payload hash and never sends the header).

**Z, authorization: `internal/acl` evaluation, `internal/policy`, tags, op
mask** (~4k). Goal: radosgw's `verify_permission` for phase 1's ops: op mask,
bucket and object ACL evaluation (grants, groups, canned ACLs, the S3 XML in
both directions), the IAM policy language (parse, principals including
tenants and accounts, S3 actions, ARNs, the condition operators s3-tests
exercises), bucket policy evaluation, evaluation of identity policies already
stored on users (decision D6), object and bucket tags with their XML and the
`s3:ExistingObjectTag` conditions. §9: "ACL, policy, quota and tenant
authorization"; the ACL, policy and tagging subresources' types and XML (the
ops that store them are M's and W's). Quota enforcement is M's (it needs the
stats caches); "the op-level check call" is Z's. Gates: unit specs from
radosgw's own policy test cases (`src/test/rgw/test_rgw_iam_policy.cc`) and
ACL fixtures; s3-tests ACL, policy and tenant groups (`test_bucket_policy*`,
`test_object_acl*`, `test_bucket_acl*`, tenant tests) once R and W land.

**M, metadata plane: zone, cache, users, buckets, listing, logs** (~5k).
Goal: `internal/driver` part 1: zone, zonegroup, realm and period resolution
from the root pool in Rook's zoned shape with the release detected once;
placement resolution to pools and namespaces; the metadata cache (users,
bucket entry points, bucket instances with attrs, zone configuration; 25000
entries, 900 s) with the watch on `notify.0-7` and the cache-notify record
sent on every metadata write; user lookups by uid, access key and email
through the index objects; bucket entry point and instance read and write
under cls_version tracking; the per-user bucket list (cls_user); bucket
create, delete, head, location and ListBuckets; ListObjects v1 and v2 over
`bucket_list` with pending-entry reconciliation and the per-release
`dir_suggest_changes` guard; bucket and user stats with radosgw's quota
caches and enforcement; the usage log accumulator and its flush worker; watch
re-registration; creation of the control objects when a fresh store lacks
them; the ACL, policy and tagging subresources on buckets. §9: "ListBuckets,
Create, Delete, Head and GetLocation for buckets, ListObjects v1 and v2";
"cache invalidation both ways"; the listing half of "the reshard protocol";
"usage log writes"; quota; the bucket subresources. Gates: the oracle matrix
on user and bucket metadata (radosgw-created users and buckets served by
rgw-go; rgw-go-created ones read by radosgw and `radosgw-admin user info`,
`bucket stats`, `user stats`); a cache-notify test in both directions
(`radosgw-admin quota set` visible to rgw-go within the notify, rgw-go's
`CreateBucket` visible to a coexisting radosgw); s3-tests bucket groups; Rook
`zonepools`.

**R, object read path** (~3k). Goal: `internal/driver` part 2: GET, HEAD and
GetObjectAttributes for every object radosgw can have written — one op
composing stat, xattrs and the first 4 MiB; tail reads windowed at 16 MiB in
4 MiB requests, streamed in order; ranges and conditionals; both manifest
forms and every placement and storage class; the four compression codecs
decoded with Ceph's own framing; cloud-tier and key-server-encrypted objects
refused as radosgw refuses them; `_`-prefixed names and their locators; the
object ACL and tagging GETs. §9: "Get with ranges and conditionals, Head,
GetObjectAttributes"; "read compatibility for every placement, storage
class, compression codec and manifest layout". Gates: the populated corpus of
the phase-0 cluster read back byte-exact through rgw-go (`plain`,
`t1/tenanted`, `multipart.bin`, `_underscore.bin`, plus objects written with
each codec on a compression-enabled placement the population script gains);
s3-tests GET/HEAD/range/conditional groups; the seam microbenchmark's read
shape; the pipeline one-round-trip handler.

**W, object write path: PUT, DELETE, COPY, GC, index completions** (~5k).
Goal: `internal/driver` part 3: PUT within and beyond the head with radosgw's
round trips (guarded prepare, guarded head write with data, manifest and
attrs, fire-and-forget complete), tails written while the body streams under
the put window and the in-flight limiter, the write tag and the atomic
write's `ECANCELED` handling (W-D1 settles it as radosgw's code has it: a
lost race cancels the index entry and answers success), the busy-resharding
re-read and retry; DELETE and DeleteObjects with GC enqueue in whichever
shard format the shard is in and `pool_full_try`; Copy sharing tails through
the refcount class under the NUL-terminated tag and rewriting only the head,
streaming only where placement, storage class or head geometry force it; the
index completion manager with its retry worker; the GC worker on its period
under `gc_process`; object ACL and tagging PUT/DELETE. §9: "Put, Delete,
Copy, DeleteObjects"; "GC enqueue and worker"; the write half of "the
reshard protocol"; the object subresources. Gates: the oracle matrix the
other way (rgw-go-written objects read by radosgw and `radosgw-admin object
stat`, `bucket check`, `gc list`; GC entries processed by whichever gateway
runs first); s3-tests PUT, DELETE, COPY and multi-object-delete groups; the
seam microbenchmark's write shape; the gateway comparison.

**P, multipart: the seven ops** (~2.5k). Goal: CreateMultipartUpload,
UploadPart, UploadPartCopy, ListParts, ListMultipartUploads,
CompleteMultipartUpload and AbortMultipartUpload with radosgw's layout: the
meta object in the placement's data-extra pool, part heads and their
manifests, the multipart namespace entries in the bucket index, the
`RGWCompleteMultipart` exclusive lock composed with `assert_exists` in one
op, manifest assembly, and abort and complete cleanup through GC. The lock is
a new class package, `internal/cls/lock`, because radosgw composes its
requests into write ops (`rgw_sal_rados.cc:4969-4976`). §9: "the seven
multipart ops". Gates: the oracle matrix on multipart (radosgw's
`multipart.bin` read through rgw-go is R's; here rgw-go's uploads read by
radosgw, and an upload started by one gateway completed by the other);
`radosgw-admin bucket check`; s3-tests multipart groups.

**N, admin API, `internal/admin`** (~4k). Goal: the `/admin` surface §9
lists, on the same op layer as S3: user (create, modify, remove, info, keys,
subusers, caps, quota), bucket except Sync_Bucket (info, list, stats, link,
unlink, remove with purge and with bypass-gc, check index, policy, object
remove, quota), usage
(show; trim with a bounded loop, since `docs/ceph-upstream-bugs.md` shows
ENODATA may never come), metadata over user, bucket and bucket.instance (get,
put, list, remove), info, account, config, ratelimit settings stored, and the
read-only realm and period getters; caps checked from the identity;
radosgw-admin's JSON shapes. The user, bucket and account operations shared
with S3 live in `internal/op`, above the driver. The bypass-gc purge deletes
object data directly as radosgw's `remove_bypass_gc` does, so its driver half
(N Task 14) calls W's, R's and P's internals and lands after them. §9: the
whole "Admin:" sentence. Gates: the go-ceph rgw/admin suite against rgw-go; the Rook
operator's own admin calls first (N Task 13 lists them); Rook
`user/{caps,keys,opmask,placement,storageclass}`, `bucket/{owner,quota}`,
`dependents`, `cosi`.

**T, gates, image and benchmarks** (~2k Go plus shell and YAML). Goal: the
machinery that judges the other eight: the seam microbenchmark in the three
completion modes with §6's op shapes; the pipeline baseline runner; the
s3-tests comparative harness (pinned clone, configs for radosgw and rgw-go on
one cluster, group selection, equal pass-and-fail-set comparison) with
radosgw's baseline recorded first; the go-ceph rgw/admin suite runner; the
derived Ceph image per release with rgw-go as `/usr/bin/radosgw` and the
goreleaser change cgo forces; the Rook nightly workflow running the phase-1
subset of the object suite on kind against the derived image; the gateway
comparison through the elbencho fork under the parity settings, with
`docs/benchmarks/`; the answer to the cgo question written into
`docs/cgo-limitations.md`; and the population change R's oracle needs
(`hack/rooket/populate.sh` writes objects with each compression codec). §9:
every gate named in the phase 1 paragraph; §11 in full; the §12 image and
Rook workflow. Gate: T is the gates; its own check is that each harness runs
green against radosgw alone before rgw-go is plugged in.

### Dependency order

The waves in "Execution order" come from this order. T has four parts: the
seam microbenchmark (T-a), the s3-tests and admin-suite harnesses with
radosgw's baselines (T-b), the derived image and Rook workflow (T-c), and the
gateway comparison, s3-tests parity and cgo answer (T-d); T's own task index
maps them onto its tasks.

- First, in parallel: G, T-a and T-b. T-a needs only the seam and the
  cluster: §6 fixes the op shapes (a read composing stat, xattrs and 4 MiB; a
  guarded write composing a class exec, data and xattrs), so the cgo question
  does not wait for the driver, and its answer informs R's and W's buffer and
  window decisions. T-b establishes radosgw's own pass-and-fail set before
  there is anything to compare it with.
- After G's first task (the interface freeze): A, Z and M in parallel. They
  share only the types G fixes.
- After M: R and N in parallel; N also needs A. N's bypass-gc purge (its
  Task 14) and its gate wait for W, R and P, whose driver internals the purge
  calls.
- After R: W. W shares the head layout, manifest generation and object state
  with R and must write what R reads.
- After W: P.
- After P's Task 7: N's Task 14, then N's gate.
- After all: T-c's suite run (the image itself needs only G's binary) and
  T-d.

Why this order, from the data path and the gates:

- Performance is objective one. The hot paths are R and W; they are scheduled
  as early as their dependencies permit, and everything they need from M is
  the minimum (zone, placement, bucket lookup, cache). The pipeline baseline
  (G) measures the HTTP layer before any RADOS cost exists, so the later
  attribution "ours versus RADOS versus cgo" has its first term early. T-a
  answers the cgo question first because §11 says it is the question the
  project studies and §7's completion-mode default rides on it.
- R before W because the oracle is strongest and cheapest there: the phase-0
  cluster already holds every layout radosgw writes, so read compatibility is
  provable with zero write code, and W then writes the shape R has proved it
  reads. The reverse order would make every W bug look like an R bug.
- M before R because a GET needs the bucket instance, placement pools and the
  cache; M is also what Rook's operator touches first through the admin API.
- Z is a soft dependency for development (ops run with a permissive stub)
  and a hard one for every s3-tests gate, so it runs in wave 1 alongside M
  rather than blocking it.
- P last on the data path because it extends W (a part is a PUT to a part
  head; completion assembles W's manifests) and adds the one data-path lock.
- N in wave 2 rather than later because the Rook suite's user and bucket
  sub-tests and the operator's reconcile loop are admin-API driven; it is the
  shortest route to the first Rook sub-test passing. Only the bypass-gc purge
  (N Task 14), which no Rook sub-test calls, waits for the data path.

Parallelism budget: three implementers at once, the phase-0 ruling; wave 1 is
exactly three units.

### Shared interfaces and seam gaps

The decomposition sketched the interfaces G's first task freezes: the request
context, identity and errors, the op lifecycle and sink, the store
interfaces, authentication, authorization, configuration and the frontend.
G's "Frozen interface contract" appendix (`G-gateway-core.md`) supersedes
those sketches, and G Task 1 records where it resolved what a sketch left
open. Two points of the sketches outlive them: the store interfaces are split
by concern, so each stays small and each unit fakes only what it uses; and
the authentication sketch's `auth.Verifier.Region` is dropped, because
radosgw never checks the credential scope (A's D-A8).

Changing the seam later is expensive, because every class package and the
driver build on it, so each gap was assigned before the units ran. The
evidence is at ceph main 7ed73efc1be.

| Gap | Evidence | Owner and resolution |
|---|---|---|
| No `Cluster.FSID()` | `/admin/info` returns `cluster_id` from `driver->get_cluster_id`, the RADOS fsid ([`rgw_rest_info.cc:29-37`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/rgw_rest_info.cc#L29-L37)) | N Task 2: `FSID(ctx) (string, error)` over go-ceph's upstream `Conn.GetFSID` |
| No `OpFlagFullTry` | radosgw sets `pool_full_try` on object deletion and GC ([`rgw_rados.cc:5575`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/driver/rados/rgw_rados.cc#L5575), `:6755`, `:7248`; `rgw_gc.cc:611`); `LIBRADOS_OPERATION_FULL_TRY = 64` ([`librados.h:130`](https://github.com/ceph/ceph/blob/v19.2.6/src/include/rados/librados.h#L130)) | W Task 1: the constant and its goceph translation to go-ceph's upstream `OperationFullTry` |
| Lock steps cannot compose with `AssertExists` | CompleteMultipart's `try_lock` is `op.assert_exists(); lock_exclusive(&op); operate` in one write op ([`rgw_sal_rados.cc:4969-4976`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/driver/rados/rgw_sal_rados.cc#L4969-L4976)); the seam's `LockExclusive` is the standalone ioctx form | P Task 1: the `internal/cls/lock` class package marshalling `lock`, `unlock` and `break_lock` over `Execer`, no seam change; `gc_process` keeps the ioctx form ([`rgw_gc.cc:494-508`](https://github.com/ceph/ceph/blob/7ed73efc1beb034b76e150439972959857600920/src/rgw/driver/rados/rgw_gc.cc#L494-L508)) |
| No in-flight limiter anywhere | §7 requires one under the objecter throttle (1024 ops, 100 MiB); `goceph/pool.go` counts in-flight operations per handle only for close | G Task 2: in goceph behind `Config.MaxInflightOps` and `MaxInflightBytes`, parked in Go (decision D8); `rgw_max_concurrent_requests` is the frontend's separate cap |
| `ListObjects` has no cursor | admin `metadata list` and `bucket list` page with `ObjectCursor` strings (`rgw_tools.cc:360-375`); go-ceph binds only the pg-hash `Iter.Token`/`Seek` ([`rados/object_iter.go:29-35`](https://github.com/jhoblitt/go-ceph/blob/cbf97f85fcf5000ca4beca4a7fbf2ee746fa965d/rados/object_iter.go#L29-L35)), not the object-list cursor | N Task 2: `ListObjectsFrom(ctx, token string, max int, fn) (next string, more bool, err error)` on a pg-hash token, with rgw-go's own opaque markers (decision D5); the token resumes by placement hash, since `Iter.Token()` is the next-batch cursor (N-D3) |
| `rados_aio_cancel` unbound | `docs/cgo-limitations.md`, open | none in phase 1; the byte budget counts an abandoned operation's pinned buffers until its completion fires |
| No pool create or `application_enable` | radosgw creates a missing pool and tags it `rgw` ([`rgw_tools.cc:32-51`](https://github.com/ceph/ceph/blob/v19.2.6/src/rgw/driver/rados/rgw_tools.cc#L32-L51)) | none: decision D4; M Task 13 records the difference in `docs/exclusions.md` |
| `ExecResult` hides a positive class return | `docs/cgo-limitations.md`, open | none: no phase-1 class method returns a meaningful positive value |
| Watch acknowledgement | goceph acks every notify ([`goceph/pool.go:485`](https://github.com/jhoblitt/rgw-go/blob/50fc45c4d59953661d5c57c27ca7016bc55edeeb/internal/radosclient/goceph/pool.go#L485)) | no gap |

### Spec ambiguities and decisions D1-D11

Real forks in the spec, each decided before the plans were written. The plans
cite them as "decision D<n>".

**D1. The phase-1 Rook gate.** §9 says "the Rook suite as soon as the derived
image serves a store", but `runObjectE2ETest`
([`tests/integration/ceph_object_test.go:103-138`](https://github.com/rook/rook/blob/v1.20.7/tests/integration/ceph_object_test.go#L103-L138) in rook) runs
`bucket/lifecycle` (a lifecycle-configuration round trip, phase 2), `topic`
and `notification` (phase 3) unconditionally and in sequence, so a full pass
is impossible in phase 1 by the spec's own phasing. Decision: the phase-1
Rook gate is the subset `zonepools`, `bucket/{owner,policy,quota,rw}`,
`user/{caps,keys,opmask,placement,storageclass}`, `cosi` (its non-TLS pass;
it skips itself under TLS) and `dependents`, run from a Rook checkout with the
other three calls removed by a patch the workflow applies; the full suite
becomes the phase-3 gate. Rejected: pulling lifecycle-configuration storage
into phase 1, which is phase 2's by §9 and touches the lc shard omap. T
adopts it (T Task 10).

**D2. How the derived image reaches Rook's e2e installer.** The installer
chooses the Ceph image only from its per-suite image constants
(`tests/framework/installer/ceph_installer.go:96-115` in rook); there is no
arbitrary-image input. Decision: the Rook workflow rewrites the Squid or
Tentacle image constant in its checkout before `go test`; the Rook-side
override the spec defers (§3) stays deferred. T adopts it and names the
constants (T's D2).

**D3. LZ4.** §9 requires all four codecs decoded on read and §4 calls its
dependency list complete, but `klauspost/compress` has zstd, snappy and
zlib/flate and no LZ4. Ceph's LZ4 plugin uses LZ4 block compression under
Ceph's own per-chunk framing (`src/compressor/lz4/`). Decision: add
`github.com/pierrec/lz4/v4` (block API only) and amend §4's list. Rejected:
an in-house LZ4 block decoder, small but a maintenance liability with no
upside. R adopts it (R Task 2); owner decision 13 accepted it, and spec
§4's list is amended on main to name it.

**D4. Pool auto-creation.** radosgw creates a missing pool and enables the
`rgw` application on it. Under Rook every pool exists before the gateway
starts, and the seam has no pool create. Decision: phase 1 creates no pool;
a missing pool is a configuration error, at startup for the root and
control pools and on the request that needs any other. G and M adopt it.
The decomposition also asked for the difference to be recorded in
`docs/exclusions.md`'s coexistence section: M Task 13 writes that entry
beside M-D1's, with radosgw's creation paths at both release tags (at
startup and on first use) and Rook's pool reconcile, which creates every
pool before a gateway starts.

**D5. Admin listing markers.** radosgw's `metadata list` and unfiltered
`bucket list` return `ObjectCursor` strings as markers; producing those from
the C API means reimplementing `hobject_t` stringification. Cross-gateway
marker continuity matters only when one client pages a listing across two
gateway implementations, which nothing in Rook does. Decision: rgw-go's
markers are its own opaque tokens, round-tripping only through rgw-go and
documented as a difference; revisit if the dashboard or an operator workflow
needs the cursor form. N adopts it (N-D3; N Task 13 records the difference).

**D6. Identity policies stored on users.** §9 names bucket policy; IAM's
user-policy management is phase 3. But radosgw evaluates a user's stored
identity policies on every request, so a user radosgw denies through an
explicit Deny in an attached policy would be allowed by an rgw-go that
ignored them — a security divergence on a shared zone. Decision: Z evaluates
the identity policies found in the user's attrs (read-only, the same engine,
small cost); the IAM ops that create them remain phase 3. Z adopts it (Tasks
9-10). Group policies are not evaluated until phase 3, so members of IAM
groups are refused meanwhile (owner decision 6, D-Z2).

**D7. Chunked-upload trailers and checksums.** §9 puts "chunked and trailer
payloads" in phase 1 and "checksums at Tentacle level" in phase 2. Current
AWS SDKs send a CRC trailer by default, which is why `populate.sh` turns it
off. Decision: phase 1 parses the trailer and verifies its signature, and
neither validates nor stores the checksum value; the s3-tests checksum groups
are excluded from phase 1's parity set; phase 2 adds the algorithms, the
`x-amz-checksum-*` attrs and GetObjectAttributes' checksum fields. A adopts
it (A Task 7). Owner decision 7 kept this phasing; on Tentacle it is a
difference from radosgw, which P Task 3 records in `docs/exclusions.md`.

**D8. Where the in-flight limiter lives.** §7 requires one but names no
package. Decision: goceph, transport-owned, rather than the driver, so that
the class packages' own operations are bounded too and the seam
microbenchmark measures the real submission path. G adopts it (G Task 2).

**D9. Metrics in phase 1.** §4 lists `internal/metrics` with no phase; §11's
benchmarks record CPU, threads and RSS externally. Decision: a minimal
Prometheus surface in G (per-op counts and latency, in-flight requests, RADOS
ops and bytes per mode), because the pipeline baseline and the gateway
comparison want the internal split; asok and the ops log stay phase 3, as §9
says. G adopts it (G Task 7).

**D10. The phase-1 s3-tests parity set.** The gate is an equal pass-and-fail
set "for the phase's groups", and s3-tests marks tests by feature, not by
rgw-go's phases. Decision: run the s3-tests functional suite excluding, by
marker, the features of phases 2 and 3 and of `docs/exclusions.md`, and keep
`fails_on_rgw` in the set, since both gateways must fail those alike; T pins
the s3-tests commit and records radosgw's set on both releases before any
comparison. T adopts it and fixes the files and the marker list (T's D10).
Tests that call a phase 2 or 3 feature without carrying such a marker (156
at the pinned commit: versioning, object lock, POST object, CORS, public
access block, policy status, checksums and `?usage`) are deselected on both
gateways by T Task 4's per-phase node-id lists, which shrink as the phases
land; the known-difference lists hold decided differences only.

**D11. The first comparison release.** §9 does not say which release the
first gateway comparison runs on. Decision: Squid first (the floor, and the
release the integration workflow's librados is built against), then Tentacle
in the same gate. T adopts it.

### The first unit

G was planned first, with T's seam microbenchmark (T-a) beside it. G is the
only unit every other unit depends on: its first task freezes `op.Identity`,
`op.Request`, `op.Error`, the `Op` lifecycle, the `Sink`, the store
interfaces and their fakes, the authentication shapes and the
`cephconf`/`frontend` contracts, after which A, Z and M dispatch to three
workers at once. It yields the first artifact with external value — a binary
that starts under Rook's exact argv, connects through librados, serves TLS
from the beast spec and answers the readiness probe — which the derived image
(T-c) and the pipeline baseline need. And it is where performance is measured
first: the baseline's no-op handler bounds what the HTTP layer costs before a
single RADOS op exists, which §11 needs as the first term of its attribution.
T-a runs in the same wave: it depends on nothing G builds, its op shapes are
fixed by §6, and its answer to the cgo question is the input R's and W's
buffer, window and completion-mode decisions should have in hand.
