# rgw-go design

Status: design approved section by section on 2026-09-25; this document
consolidates it for review before planning. Companions: `docs/exclusions.md`,
the canonical excluded-features list shared with rgw-rs, and
`docs/differences.md`, which holds the coexistence obligations and the
benchmark parity settings. Claims about C++ RGW, Rook and go-ceph behavior
in all three documents were checked against ceph/ceph main at e234256339f
and the v19.2.3 tag, Rook main at d364b1e8a, and go-ceph v0.39.0.

## 1. Goal

rgw-go is a reimplementation of Ceph's RADOS Gateway in Go 1.27 over
ceph/go-ceph, with go-ceph improvements made in the jhoblitt/go-ceph fork.
Its goal, in the user's words, is to be a drop-in replacement for C++ RGW
in a Rook cluster running Squid or Tentacle.

The acceptance criterion is explicit: rgw-go, installed in a derived Ceph
image as `/usr/bin/radosgw`, passes Rook's object integration suite,
`TestCephObjectSuite`, in both of its passes, with and without TLS, against
current Rook main. That suite's shared store is Rook's zoned shape, a single
zone declared through the realm, zonegroup and zone resources with shared
pools mapped as RADOS namespaces, plus a classic store for the dependents
test. Multi-zone sync tests are excluded and the current suite has none;
the keystone suite is excluded because Keystone is; the helm and upgrade
suites test Rook rather than the gateway.

Beyond the criterion, rgw-go is benchmarked against C++ radosgw and rgw-rs
on the same cluster, and the project evaluates whether go-ceph's cgo
boundary is a performance bottleneck a pure-Go RADOS client would remove.
The evidence accumulates in `docs/cgo-limitations.md`.

Since 2026-10-31 rgw-go also aims to be more hardened than radosgw,
suitable for exposure to the commodity Internet. Section 15 states the
threat model and the rules that follow from it.

## 2. Objectives, in priority order

1. Performance: latency and throughput on the S3 data path.
2. Scalability: more concurrent requests with fewer OS threads per process.
3. A compact, maintainable code base, leaning on existing Go packages
   wherever that does not cost the first objective.

Measurable forms: identical RADOS round-trip counts to radosgw on both hot
paths, so any difference is attributable to the pipeline, the HTTP layer or
the cgo boundary; an OS thread count under load of GOMAXPROCS plus
librados's own threads plus a handful, independent of connection count,
against radosgw's 128-thread request pool; and a measured, not assumed,
cost for the cgo boundary.

## 3. Decisions and reasons

- **Bit-compatible with radosgw's RADOS layout.** Same pools and
  namespaces, same object naming, same object-class index protocol, same
  versioned encodings. Checked against the C++ write and read paths: the
  layout costs little on the hot path, so a new layout would confound the
  benchmark rather than win it; the same layout isolates the variable this
  project studies; and radosgw becomes the correctness oracle. The
  unmodified radosgw-admin administers rgw-go's metadata, so no CLI is
  reimplemented.
- **Ceph floor Squid 19.2.7 or Tentacle 20.2.4, feature level Tentacle
  where gateway-side.** Every daemon of a supported cluster, its OSDs and
  any radosgw sharing the zone included, runs 19.2.7 or later on Squid or
  20.2.4 or later on Tentacle. The floor governs which OSD-side class
  behavior may be assumed, and point releases below it get no special
  handling. It constrains running daemons, not data: metadata, index
  entries and queue state written by any earlier release are still decoded,
  and damage an older release left behind, such as queue accounting drift,
  is still tolerated. Feature level has a precise form through the encoding
  rule in section 8.
- **Own gateway core, borrowed edges.** An RGW-shaped op pipeline on
  net/http, one flat store interface, one RADOS driver, and a narrow RADOS
  client seam over go-ceph. Rejected: a wholesale port of RGW's class
  hierarchy, whose storage handle hierarchy exists for six backends and
  collapses with one; and assembling on an existing Go S3 frontend, which
  would put a framework we do not control between the socket and the hot
  path and would need a patch series to reach radosgw's observable
  behavior.
- **Ceph encoding primitives and object-class clients live in rgw-go, not
  go-ceph.** Ceph itself packages every class client as an RGW-internal
  static library with no installed header; only cls_lock is generalized,
  and that by librados embedding it. Only librados bindings go to the fork.
- **The class packages and the encoding primitives depend only on each
  other and on the seam's exec-step abstraction, never on RGW types or
  the driver.** The one leak in the C++ headers, the user identity type in
  usage entries, is handled by the class package owning a small copy.
- **The binary reaches the pod through a derived Ceph image** that
  replaces `radosgw`, because Rook has no per-daemon image override. A
  Rook-side override is a later step.
- **Exclusions** are decided by the three tests in `docs/exclusions.md`
  and recorded there with their coexistence obligations.

## 4. Architecture

One binary, one module, `internal/` throughout. Dependencies point downward.

| Package | Provides |
|---|---|
| `cmd/rgw-go` | `main`, signal context, nothing else |
| `internal/cli` | cobra tree with `serve` and `version`; the radosgw-argv mode |
| `internal/cephconf` | early-argument parsing, the librados config bridge, typed access to `rgw_*` options |
| `internal/frontend` | `http.Server` instances from the beast frontend spec, TLS, per-request deadlines, drain on shutdown |
| `internal/s3`, `internal/admin`, `internal/iam` | protocol layers: dispatch, parsing, response and error documents |
| `internal/op` | the ops, one type per operation, with the store interfaces they consume |
| `internal/auth`, `internal/policy`, `internal/acl` | signature verification with chunked and trailer readers; the IAM policy language; ACL semantics and encoding |
| `internal/driver` | the RADOS store: zone and placement resolution, object naming, manifests and striping, atomic head writes, the index protocol, GC enqueue, metadata cache with watch and notify, quotas, usage |
| `internal/cls/<class>` | one package per object class: request and response marshalling over the exec step |
| `internal/meta` | RGW's stored types with their encodings: identity primitives, metadata objects, zone configuration, manifest and compression info |
| `internal/denc` | Ceph encoding primitives |
| `internal/radosclient`, `internal/radosclient/goceph` | the seam, and its implementation over go-ceph, the only package importing it |
| `internal/metrics`, `internal/asok`, `internal/opslog`, `internal/version` | Prometheus counters; the admin-socket protocol with RGW-shaped counters; the ops log; build info |

Rules inside the table:

- The seam is a leaf package, an accepted departure from declaring
  interfaces at the consumer: it has two consumers, the driver and every
  class package, and one implementer.
- `op` owns the store interfaces, split by concern so each stays small,
  with counterfeiter fakes beside them.
- Types that carry semantics live with their semantics: ACL policy with
  grant evaluation, lifecycle, CORS, website, object lock, tags, public
  access block and encryption configuration each beside their evaluation
  and XML. `meta` holds only what is stored and looked up.
- The op layer is protocol-neutral: S3, the admin API, IAM and STS share
  the user, bucket and account operations, lifted above the driver as
  rgw-rs found they should be.
- S3 dispatch is a table matched on method, scope and subresource in
  radosgw's precedence, after Host, path and query are parsed once. No
  path router: none can express subresource dispatch or wildcard hosts,
  and the one that can match query keys scans regex routes linearly.

Departures from the Go house canon, each with its reason: cgo in the
unit-test suite and CI, because go-ceph is cgo; a derived Ceph image
instead of ko on distroless, because the binary needs librados; zero
`ReadTimeout` and `WriteTimeout` with per-request deadlines through
`http.ResponseController`, because objects are large; and no path router.

Third-party dependencies, complete: go-ceph, cobra, viper,
klauspost/compress, pierrec/lz4/v4, the Prometheus client,
cespare/xxhash/v2, x/sync, Ginkgo and Gomega, counterfeiter, and
aws-sdk-go-v2 plus go-ceph's rgw/admin as test clients. pierrec/lz4/v4
decodes LZ4 in Ceph's per-chunk framing in phase 1 and encodes it in
phase 2, because klauspost/compress has no LZ4 codec; cespare/xxhash/v2
computes the XXH64 that radosgw uses to pick a GC shard, and the
Prometheus client already requires it. Everything else is standard
library.

## 5. Configuration and invocation

Rook launches the `radosgw` binary with `--foreground`, `--id`, `--host`,
`--setuser`, `--setgroup`, mon and keyring flags, `--rgw-enable-apis`, the
beast frontend spec, and optional ops-log flags, and sets `rgw_zone`,
`rgw_zonegroup`, usage-log and logging options through the mon config
store. rgw-go therefore accepts radosgw's argv: when invoked as `radosgw`,
`cli` rewrites argv into `serve -- <args>` so cobra stays the single entry
point, and `cephconf` does what ceph's `global_init` does. It consumes the
early arguments ceph handles before config, `-c`, `--cluster`, `-i`, `-n`,
`--no-config-file` and `-v`, creates the connection under that name, reads
the config file and `CEPH_ARGS`, and hands everything else to librados,
which already handles `-f`, `-d`, `--keyring`, `--mon-host`,
`--no-mon-config`, `--setuser` and every `rgw_*` option. Privileges are
dropped after the frontend binds, not before connecting: like radosgw,
`cephconf` defers the drop, connects to RADOS as the launching user, and
the frontend drops to `--setuser`/`--setgroup` only after binding, so it
can bind privileged ports. librados applies the mon config store during
connect, so options Rook set centrally are readable afterwards.

Ceph config is the single source for everything radosgw configures.
rgw-go's own flags cover only what has no ceph option: `--log-level`,
`--log-format`, `--metrics-addr`, `--rados-completions`,
`--memory-limit-ratio`, `--memory-budget-ratio`, and viper's `--config`.
The two memory flags are the precedent section 15's rule for budget flags
follows: they size the Go memory limit and the buffer budget from the
memory the pod or host has, which no Ceph option knows, and
`--memory-budget-ratio` refuses 0, the one value that would leave the
buffers unbounded. The frontend parses the beast spec keys `port`,
`ssl_port`, `endpoint`, `ssl_endpoint`, `ssl_certificate`,
`ssl_private_key`, `ssl_options`, `ssl_ciphers`, `tcp_nodelay`,
`request_timeout_ms`, `max_connection_backlog` and `max_header_size`;
unknown keys log and are ignored. `rgw_enable_apis` is honored; swift and
swift_auth are ignored with one log line. Logging is slog, JSON to stderr,
as radosgw in Rook logs to stderr.

## 6. Data path

One goroutine per request. The frontend stamps radosgw's transaction-id
format; the dispatcher selects the op; auth resolves the credential through
the metadata cache and, for chunked uploads, wraps the body in a reader
that verifies each chunk signature as bytes stream; the op runs radosgw's
lifecycle in order: load metadata, verify permission through op mask, ACL,
policy and quota, execute, complete, respond.

| Path | Round trips on the latency path | Parallelism |
|---|---|---|
| PUT within the 4 MiB head | index prepare with the reshard guard, then the guarded head write with data, manifest and attributes; the index complete is issued and not awaited | none |
| PUT beyond the head | 4 MiB tail stripes written while the body streams, then prepare, then head | tails in flight up to the put window, 16 MiB |
| GET within the head | one read op composing stat, xattrs and the first 4 MiB | none |
| GET beyond the head, or a range | the first op, then tail reads | up to 16 MiB in flight in 4 MiB requests, streamed in order |

These are radosgw's round trips and radosgw's default windows. The
fire-and-forget index complete follows radosgw's completion manager, with
retries by a worker and reconciliation of a leftover pending entry at the
next listing. A failed head write cancels the prepare. A lost race on the
guarded head write, an `ECANCELED`, `ENOENT` or `EEXIST`, cancels the index
entry and answers success when the request carried no precondition, as
radosgw's does. Copy shares tails through the refcount class under the new
tag with the NUL-terminated form radosgw writes, rewriting only the head,
and streams data only where placement, storage class, encryption or head
geometry force it. CompleteMultipart takes radosgw's named lock on the
multipart meta object. Listing performs radosgw's pending-entry
reconciliation.

## 7. Concurrency

- **Completions wake goroutines; three mechanisms are built and
  selectable** by `--rados-completions`: `sync`, stock go-ceph's blocking
  operate, the thread-per-operation baseline; `callback`, a C callback
  into Go that signals the waiter's channel, the default; and `pipe`, a C
  callback writing an 8-byte id to a pipe that one goroutine drains through
  the netpoller. librados runs callbacks on its single finisher thread, so
  they are serialized regardless. A seam-level microbenchmark measures
  submit-to-wake latency, completions per second and OS threads per mode.
- **Buffers are pinned, not copied**, with `runtime.Pinner` for the life
  of the operation. librados copies once in each direction inside the C
  API, which radosgw also pays.
- **Submission never blocks an OS thread.** librados's objecter throttle,
  1024 operations or 100 MiB, blocks the submitting thread inside C.
  rgw-go keeps its own in-flight limiter under those limits so waiting
  happens parked in Go, and honors `rgw_max_concurrent_requests`, default
  1024, as a hard cap. One memory budget, half the Go memory limit by
  default, bounds the buffers of object data every GET, PUT and copy
  holds; a request that finds it spent waits (`docs/differences.md`,
  "Object data buffers share one memory budget, sized from the Go memory
  limit").
- **Cancellation.** A client disconnect cancels the request context and
  every wait selects on it. librados can only abandon an in-flight
  operation on the client side (`rados_aio_cancel`, not yet bound in the
  fork), and a write already sent to the OSD may still apply, so the
  completion owns the pinned buffers and never touches the response.
- **Metadata cache.** Decoded users, bucket entry points, bucket instances
  with attributes, and zone configuration, 25000 entries with radosgw's
  900 second expiry, watching the eight control-pool notify objects and
  sending the same notify on every metadata write rgw-go performs.
- **Workers**, each an errgroup goroutine stopped by the root context:
  phase 1 has the GC processor on its one-hour period with the `gc_process`
  locks, the usage log flush every 30 seconds or 1024 entries, the index
  completion retry worker, watch re-registration, and quota statistics;
  phase 2 adds lifecycle; phase 3 adds the notification deliverer and the
  persistent-queue expiry pass.

## 8. Compatibility and encoding

`denc` implements the wire encoding RGW and its classes use: little-endian
integers, length-prefixed strings and bufferlists, the standard containers,
time, and both struct framings, the standard one and the legacy
compat-length one that sixty decoders in scope use. No feature-dependent
encoding exists in this scope. On decode a struct whose compat byte is
above our version is refused and trailing unknown fields are skipped; on
encode the length is patched when the frame closes.

The version rule, by layer:

- Driver-level persistent types, the ones the gateway reads and writes as
  object data, xattrs or omap values: decode every struct version the C++
  decoders accept, since never-rewritten metadata sits at its original
  encoding; encode at the version the cluster's own radosgw release
  writes, detected once from the required OSD release and overridable by
  flag. Coexistence is byte-exact by construction.
- Class requests: encode at the same cluster-release version, so request
  shapes added in Tentacle are reachable on Tentacle OSDs.
- Class replies: decode from the Squid version upward, because the OSD's
  class re-encodes stored records into replies at its own version. The
  `bi_*` entries that return raw stored bytes are out of scope.

Consequence: a Tentacle gateway-side feature is available on a Squid
cluster when its persistent form is a separate attribute, as checksums
are, and unavailable when it is a field of a struct the cluster's radosgw
would re-encode without it, as bucket logging is.

Class packages call only methods Squid's classes register; the four index
methods that exist only in main are never used. Methods that modify and
return, above all the 2pc queue reserve, run through a read op with the
return-vector flag, a fork item that gates persistent notifications. The
driver reproduces radosgw's placement of everything, pools and namespaces
from the zone parameters in the root pool including Rook's shared-pool
namespaces, metadata names in every namespace, index shard names across
layout generations, head, tail and multipart names with radosgw's escaping,
attribute names, and the control, log, GC, usage, lifecycle and
notification objects. The spec states these as constraints proven by the
oracle, not as string formats. The coexistence obligations that hold
regardless of any exclusion, cache invalidation, GC formats, the reshard
guard, OLH epochs, compression codecs, user statistics, lock names and
radosgw's signature quirks, are in `docs/differences.md` and are
requirements of this design.

Compatibility means RADOS state, in both directions. rgw-go serves whatever
objects, xattrs, omap entries and index state radosgw wrote, and radosgw
reads and serves whatever rgw-go writes. It does not mean reproducing
radosgw's defects. When upstream Ceph behaves wrongly, or surprisingly, in
a way a correct client meets, rgw-go does the correct thing for the S3 API
as long as the RADOS state it writes stays compatible. It reads correctly
whatever state the defect leaves behind, or states why it is unaffected.
Where a radosgw defect could corrupt data the two gateways share, rgw-go
acts to prevent the corruption, even though radosgw allows it. A serious
defect is reported upstream once a running release reproduces it. Where
rgw-go's correct behaviour differs from radosgw's at the S3 API, the
difference is recorded in `docs/differences.md`, and the parity check's
known-differences file lists it. The choice is recorded per defect in
`docs/ceph-upstream-bugs.md`, together with the affected releases, evidence
at a named release tag, and the defect's upstream status. The registry is
maintained for the life of the project: an entry is added whenever work
meets a defect and updated when upstream fixes it. It distinguishes defects
from intended behaviour that surprises a reimplementation, and leaves
go-ceph's defects to `docs/cgo-limitations.md`.

Every defect the project finds is public. It goes into
`docs/ceph-upstream-bugs.md` with its security class and its reproduction
status, which stays unreproduced until a running release shows the defect.
A possible security defect is also handed to the rgw-bug-reproduction
session. No defect is held back from the registry for disclosure, and the
registry links no private disclosure material.

## 9. Phases and gates

Every phase gate includes writing with one gateway and reading with the
other on a shared zone, s3-tests parity with radosgw for the phase's ops,
and a benchmark run.

**Phase 0, foundations.** Repository and CI. `denc`. `meta` decoding every
version and encoding per the rule, against corpus goldens. Class packages
for rgw, user, version, refcount and rgw_gc. The seam, its go-ceph
implementation in all three completion modes, and the fork bindings.
Disposable Squid and Tentacle clusters with a radosgw and a population
script. Gate: every metadata object a populated radosgw wrote decodes and
re-encodes byte-identically.

**Phase 1, data path, auth and core admin.** SigV4 header,
query and presigned with chunked and trailer payloads, SigV2, anonymous;
ACL, policy, quota and tenant authorization; ListBuckets, Create, Delete,
Head and GetLocation for buckets, ListObjects v1 and v2; Put, Get with
ranges and conditionals, Head, Delete, Copy, DeleteObjects,
GetObjectAttributes; the seven multipart ops; ACL, policy and tagging
subresources; read compatibility for every placement, storage class,
compression codec and manifest layout; the coexistence machinery, cache
invalidation both ways, GC enqueue and worker, the reshard protocol, usage
log writes; TLS through the beast spec. Admin: user with keys, subusers,
caps and quota; bucket except Sync_Bucket; usage; metadata over user,
bucket and bucket.instance; info; account; config; ratelimit settings
stored; the read-only realm and period getters. Gates: s3-tests parity, the
go-ceph rgw/admin suite, and the Rook suite as soon as the derived image
serves a store. The seam microbenchmark's preliminary answer to the cgo
question is phase 1's. Its final form, the pipeline baseline and the first
gateway comparison moved out of phase 1 on 2026-10-12, so that phase 1
closes on its correctness gates; they run on a quiet host before phase 2's
tasks start.

Phase 1's s3-tests parity is held on every pull request, not only at the
gate: it is a required check on each pull request that touches the S3
path. The check runs the phase's set against rgw-go on a disposable Squid
cluster and diffs the outcomes with radosgw's recorded Squid baseline. Any
unexpected difference fails the pull request, in either direction, a test
rgw-go fails where radosgw passes or passes where radosgw fails, because
the gate is an equal pass and fail set. Tests the baseline marks unstable
are not compared, so any unexpected difference means any difference outside
that set. A difference the release's
known-differences file lists, each entry backed by a `docs/differences.md`
entry, does not fail it. There is no s3-tests nightly. Tentacle parity is
not in the per-pull-request check: it stays the local gate run plus a
manual run in CI, a default the owner may still change. The check is
affordable because its cost is the cluster's bring-up, not the suite: a
local run of the set's 416 tests against rgw-go on a running cluster took
about 130 seconds.

Improvements to s3-tests are in phase 1's scope. ceph/s3-tests takes no
more changes: the suite lives in ceph/ceph, under `src/test/rgw/s3-tests`.
The harness pins, by commit, the `rgw-go-s3tests` branch of the
jhoblitt/ceph fork, ceph/ceph `main` with the improvements on top, as
go-ceph is pinned through jhoblitt/go-ceph. Each improvement lands on that
branch first and will eventually be proposed to ceph/ceph as a pull
request; it need not be proposed when it lands, and each upstream post
waits for the owner's explicit go-ahead. The first candidates, found
by a local run: one failed cleanup fails every later test, because
`nuke_prefixed_buckets` raises on a leftover bucket; cleanup always deletes
with a `VersionId`, so it needs versioning support;
`test_bucket_concurrent_set_canned_acl` checks only that its calls succeed;
and the `test_*_bad_expect_mismatch` tests require success for
`Expect: 200`, which RFC 9110 lets a server refuse with 417. The branch
holds only rgw-go's own changes on the upstream base: it never re-creates
or alters an existing upstream commit.

**Phase 2, versioning, lifecycle and bucket configuration.** Versioning
with the OLH protocol, ListObjectVersions, object lock with retention and
legal hold, lifecycle with the worker for expiration, noncurrent
expiration, multipart abort and in-cluster storage-class transition, CORS
with preflight, static website, the encryption configuration ops behaving
as an unconfigured radosgw, ownership controls,
public access block, policy status, POST object, checksums at Tentacle
level, compression and placement on the write path, MFA, the `?layout`
extension. Admin adds the otp metadata section.

**Phase 3, remaining services and workers.** Bucket notifications in full
except Kafka and AMQP delivery: topic actions, the notification
subresource with its filter grammar, event generation, synchronous HTTP
and HTTPS delivery, persistent topics over the 2pc queue class with an
HTTP deliverer and the expiry pass, roughly four thousand lines, gated by
Rook's notification and Kafka tests. IAM with its full action set; STS
AssumeRole, AssumeRoleWithWebIdentity and GetSessionToken; S3 Control;
bucket logging, on Tentacle clusters per the encoding rule; the `?usage`
extension; rate-limit enforcement; ops log to a file in radosgw's format;
admin-socket counters in the form ceph-exporter scrapes, with librados's
own client socket kept out of the shared socket directory. Admin adds the
roles, user_policy, group and topic metadata sections.

Bucket logging names each request by its op's `canonical_name`, which
v20.2.4 gives every op and v19.2.7 lacks. The default is
"REST.<method>.<op name>"
([rgw_op.h:312-316](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_op.h#L312-L316)),
and about 65 ops override it with their own "REST.<method>.<RESOURCE>",
"REST.GET.WEBSITE" for instance. Only bucket logging reads it
([rgw_process.cc:472-484](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L472-L484)
and the `log_record` calls in `rgw_op.cc`), so until bucket logging is
served rgw-go needs none and no exclusion records its absence. The
bucket-logging task adds it for every op at once, as an `Op` method or as
a table keyed by the op's name and the request method. An op it missed
would log under the default without error, where radosgw logs its own
name.

**Phase 4, customer-key encryption and request payment.** SSE-C,
byte-compatible with radosgw's chunked AES-256-CBC so that each gateway
reads what the other wrote, ranges, multipart parts and copies included,
gated by that cross-gateway check in both directions and by s3-tests' SSE-C
tests, over TLS or with `rgw_crypt_require_ssl` off; and the
request-payment configuration ops, whose requester-pays enforcement phase 1
already carries. Both moved here from phase 2 so that phase 2 stays smaller
than phase 1 turned out to be. Until phase 4, SSE-C reads and writes answer
501 and the request-payment ops answer 501, so nothing asked to be
encrypted is stored in the clear and no ciphertext is served.

## 10. Testing

| Layer | Needs | Covers | Runs |
|---|---|---|---|
| Unit specs, Ginkgo and Gomega | Tentacle's builder, no cluster | `denc`, `meta`, class marshalling, auth, policy, ACL, the dispatcher, every op against counterfeiter fakes, the frontend in-process | `make test`, every PR |
| Corpus and vector goldens | nothing at run time | every encoding in scope: each corpus object decodes to the JSON ceph-dencoder dumps and re-encodes to the bytes ceph-dencoder produces; generated vectors for types the corpus lacks and for Squid-version encodings; nondeterministic types compare decoded values | `make test`, every PR |
| Integration specs, `//go:build integration` | a disposable cluster | the seam in all three modes, the driver's layout and protocols, the live oracle matrix against radosgw, the admin API through go-ceph's rgw/admin client | both releases on every PR unless every file it changes is one no step reads, docs, records, the other workflows' files and other tools' config; on demand and at gates |
| s3-tests parity | a disposable cluster, radosgw's recorded baseline | the phase's s3-tests set against rgw-go, diffed with radosgw's outcomes | Squid on every PR touching the S3 path; Tentacle at gates and on demand |
| Rook end to end | kind, Rook, the derived image | the object suite, both TLS passes | nightly and at gates |

The corpus archives in the ceph checkout span 15.2 through 19.2. Goldens
are regenerated by a make target that runs ceph-dencoder from the Ceph
image; `go test` never needs it. s3-tests is comparative: the same suite
against radosgw and rgw-go on a cluster of the same release with the same
configuration, radosgw's outcomes recorded as the release's baseline, and
the gate is an equal pass and fail set for the phase's groups, less the
differences the release's known-differences file lists. The
disposable cluster is a one-worker Rook cluster on kind per release, which
rooket stands up from a released Rook with the release's Ceph image pinned,
each under its own name so that it collides with no other cluster on the
host. It is host-networked from its first bring-up, because the specs run
librados and S3 clients on the host and Rook cannot move a running cluster
onto host networking. Rook names the object store's realm, zonegroup, zone
and pools after the store, so the population derives them from the running
radosgw, and radosgw-admin always names the site, since without it
radosgw-admin creates and works in a zone the radosgw never serves. Rook's
operator writes the realm, zonegroup, zone and periods with the Ceph of its
own image, which can be a newer release than the cluster's, so the gate
holds such an object to the writing release's encoding and to what the
cluster's own ceph-dencoder writes back. The ambient cluster is never used.

Section 15 adds two layers to these: a fuzz target for every parser of
untrusted bytes and, if the owner confirms it, every decoder of stored
state, whose seeds run in `make test` and whose longer runs are scheduled;
and a denial-of-service suite that pins its resource budgets.

## 11. Benchmarking

Three benchmarks answer three questions. The seam microbenchmark answers
the cgo question. A pipeline baseline, a no-op handler and a
one-round-trip handler under the real load, answers how much of a request
is ours. The gateway comparison, radosgw, rgw-go and rgw-rs one at a time
on the same disposable Rook cluster through the derived image under the
parity settings in `docs/differences.md`, answers the project's question:
small-object PUT and GET at rising concurrency for latency percentiles,
large-object throughput, and listing at scale.

The load generator is the jhoblitt/elbencho fork at a pinned commit of its
`s3-error-counts` branch, chosen because it is a native, distributed
storage benchmark whose fork adds per-phase S3 error and retry counts to
its CSV and JSON results, a request timeout option, and ready profiles for
S3 read bandwidth, read IOPS, read latency and write bandwidth. Every
gateway is driven by the same client build. Recorded per run: p50, p99 and
p999 latency, throughput, CPU time per request, OS threads, resident
memory, error and retry counts, and for rgw-go the GC pause profile and a
CPU profile with the cgo boundary attributed. Results and their exact
commands live under `docs/benchmarks/` with the cluster description.

## 12. Repository, CI and release

Public repository `jhoblitt/rgw-go`, license LGPL-2.1-or-later as rgw-rs,
created per the GitHub canon: `gh repo create --public`, the default-branch
ruleset, an empty root commit on `main`, and every file through a draft
`init` pull request. Module `github.com/jhoblitt/rgw-go`, `go 1.27`, the
Go canon's Makefile, CI, lint config, cobra root, version package and
CLAUDE.md block. go-ceph is consumed through a `replace` to the fork at a
pinned commit until the bindings are upstream, bumped by hand.

CI runs the template's test, lint, checks and govulncheck jobs, and CodeQL,
in Tentacle's builder, the Ceph image with librados's headers and the Go
toolchain, and adds a job that compiles and links everything in Squid's;
those jobs never connect to a cluster. No runner installs a Ceph package.
Three added workflows, and a fourth that section 15 adds to run the fuzz
targets on a schedule.
An integration workflow, on pull requests and on demand, brings up the
disposable Rook clusters on the runner with a pinned rooket, one job per
release, and runs the integration specs, the oracle matrix and the phase 0
gate. Its filter is a skip list rather than a path list: a pull request
skips the clusters only when every file it changes is one no step reads,
docs, records, the other workflows' files and other tools' config, so a
file the list does not name, a new one included, runs them. Like the
s3-tests check below, it always starts, passes without work when it
skips, and is judged by a job of its own, the check to require, which
reads each release's job on its own. A Rook workflow, nightly and on
demand, builds the derived image and runs the object suite on kind; it
does not gate pull requests.

The third, an s3-tests workflow on pull requests and on demand, is the
required check of section 9. Its path filter is the S3 path: all of
`internal/` and `cmd/`, `go.mod` and `go.sum`, the s3-tests, rooket and
parity harnesses in `hack/s3tests`, `hack/rooket` and `hack/parity`, the
s3-tests test data, and the check's own workflow and cluster action, so a
change to the check itself runs it. Every internal package can change what the S3 path
answers, so only changes to docs and records skip the check. The check
always starts, and reports success without work when a
pull request leaves those paths untouched, because a required check that a
path filter keeps from starting stays pending and blocks the merge. It
brings up its Squid cluster through the same composite action as the
integration workflow, so that the two cluster setups cannot drift, and
diffs rgw-go's outcomes with radosgw's recorded baseline under the
release's known-differences file. An input of its manual run records
radosgw's outcomes in CI, for review before anything is committed: each
changed outcome carries the reason `unreviewed:` and the run, which the
ledger checks refuse until a reviewer replaces it. The baseline is an
append-only ledger: every outcome carries the s3-tests commit and the Ceph
version it was recorded at, an entry is never edited, and an entry that
changes a test's outcome or its unstable flag, or retires it, carries the
reason for it, so a regression cannot pass into the baseline unseen. A
required check runs the regression test's integrity checks on every pull
request, against honest mistakes: it keeps the baseline append-only,
requires a reason for a changed outcome, keeps a pin bump that is not
purely additive from carrying old outcomes over, and keeps pins on
jhoblitt/ceph's rgw-go-s3tests history; it lists each changed outcome with
its reason for review. A pin bump that only adds tests, proven so against
the suite's history, records just those tests; any other change to the suite, or a Ceph version bump,
records every test again as new entries. Tentacle parity runs in CI only on
demand.

rgw-go supports running only against librados 19.2.7 or later on Squid, or
20.2.4 or later on Tentacle. This is a support policy: technically the
floor binds only when the key rgw-go presents is AES256KRB5, since 19.2.6
and 20.2.4 are the first releases able to parse that cephx key type, and
an older librados fails to connect with one. The policy follows from where such keys
come from. Ceph's mkfs default at those releases allows only AES256KRB5, so
a freshly created cluster's keys have that type. Rook allows both AES and
AES256KRB5, but makes AES256KRB5 the preferred type, which new daemon keys,
the RGW's included, take unless the cluster's `cephx.daemon.keyType`
overrides it. The key types alone would not require a cluster floor, since
a cluster upgraded from an older release keeps its AES keys working; the
cluster floor in section 3 is the same releases, for OSD-side behavior.
Under Rook the derived image is every daemon's image, so rgw-go's librados
is always the cluster's own release, and a supported derived image implies
a cluster at the same floor. The integration workflow installs librados at
the floor; the headers used for building are unaffected.

Release keeps goreleaser for tags, archives, checksums, SBOM and keyless
signing, but the build is cgo in a container stage on the same base as the
target Ceph image, and the artifact is a derived Ceph image per supported
release with rgw-go as `/usr/bin/radosgw` and the original kept beside it,
published to ghcr.io under this repository, tagged with the release and the
base. `.git` is present at build time so the version stamp survives, and
the release job asserts the stamp equals the tag.

The repository CLAUDE.md carries: cgo and librados-dev, the go-ceph replace,
never the ambient cluster, Opus workers for implementation, and the
obligation to notify the rgw-rs session when `docs/exclusions.md` or
`docs/differences.md` changes.

## 13. go-ceph fork work

Thin cgo wrappers, each with a test in go-ceph's style, offered upstream
after they prove out: the async completion object and asynchronous operate
for read and write ops, with the three completion mechanisms behind an
option; read-op steps `getxattrs`, `stat2`, `cmpxattr` and omap key
listing; write-op steps `cmpxattr`, `rmxattr`, `append`, `zero`,
`truncate`, `set_flags` and omap compare; and an exec path that returns
data from a modifying class method through the return-vector flag.

## 14. Risks and items to verify at implementation

- cgo pointer lifetime across asynchronous operations is the class of bug
  that is silent until it corrupts; the seam's tests run under the race
  detector and with cgo checks enabled.
- The return-vector exec binding gates persistent notifications.
- The base OS of each Ceph release image, which fixes the builder stage.
- The librados version the CI distribution ships. Realized: Ubuntu noble's
  librados is below the supported floor in section 12, so the integration
  workflow took librados from download.ceph.com, while the jobs that never
  connect kept noble's headers. When `ubuntu-latest` moved to 26.04, where
  download.ceph.com's noble packages do not install, CI moved into the
  release builders instead: every job that links librados runs the
  release's own, and the integration workflow runs rgw-go from the derived
  image.
- The librados floor in section 12 couples rgw-go to the cephx key types
  Ceph issues. Only a pure-Go RADOS client that implements the AES256KRB5
  cipher (RFC 8009 AES256-CTS-HMAC-SHA384-192) removes the coupling. Under
  Rook the coupling costs nothing extra: the derived image is every
  daemon's image, because Rook has no per-daemon override for the RGW, so
  rgw-go's librados always matches the cluster's release and meets the
  floor whenever the cluster can issue such keys. It binds a gateway run
  outside that image, such as a host process against a fresh cluster.
- The fixed 4 KiB reservation radosgw makes per persistent event, with a
  re-reservation at commit for larger events, reported by rgw-rs and to be
  read from the code when phase 3 begins.
- The persistent-queue hazards recorded in `docs/exclusions.md`, which
  rgw-go can avoid triggering but not fix: a Squid radosgw's queue registry
  listing never pages, and reservation drift accrued under a release below
  the floor survives the upgrade.

## 15. Threat model and hardening

On 2026-10-31 the owner added a goal beside section 1's: rgw-go is to be
more hardened than radosgw, aiming at being suitable for exposure to the
commodity Internet. This section is that goal's design: what the claim
covers, what it protects and from whom, where untrusted bytes enter, the
rule for departing from radosgw on security grounds, and the budgets,
reviews and tests that back the claim. rgw-go keeps one behaviour. A
separate "Internet" profile beside a "compatibility" one was proposed and
rejected, so every hardening below applies to every deployment. A rule
marked "proposed" is a drafting decision the owner has not yet made;
"Decisions for the owner", at the end, gives each with its reason. The
owner decided the kinds of budget, how a connection past the cap is
refused, the deadline on waits for the cluster, how probes are answered at
the cap, the egress rule for connections a tenant directs, the fuzzing
time bound and the handling of notifies on 2026-11-06. Where a rule needs
code that does not exist yet, the text says what holds today beside it.

### Goal and claim

The claim: an rgw-go gateway at its defaults, serving a supported cluster,
may listen on an address the Internet reaches with no filtering proxy in
front of it, and then:

- no request, signed or anonymous, is granted what the stored ACLs,
  policies, public access blocks, object lock, MFA delete and admin caps
  deny, and no tenant reads or changes another's data, metadata or
  credentials beyond what those grant;
- no request ends or wedges the process, and no request makes rgw-go send
  the cluster input known to fault an OSD or to corrupt the RADOS state it
  shares with radosgw, except where a class's defect fires on state rgw-go
  can neither see nor change, which the registry names as the exposure
  that remains (Defence of the cluster);
- what one request or one client can hold, memory, connections,
  goroutines, time, and RADOS operations in flight, is bounded by a budget,
  so that a client's load costs service in proportion to what it sends
  rather than taking the gateway down for every tenant;
- no secret, a secret key, a signing key, an MFA seed or the gateway's own
  cephx key, leaves through a response, a log line or a metric.

"More hardened than radosgw" has a measurable form: every security-rated
entry of `docs/ceph-upstream-bugs.md` that rgw-go could meet has rgw-go
unaffected, or mitigated with the exposure that remains named, and a test
pins it (Assurance); and every budget below is enforced, those radosgw
lacks included.

The claim is a target, not a statement about today's code; when it is met,
and who says so, is proposed decision D5. It does not cover:

- Volumetric denial of service, floods that fill the network or the host
  before a request is parsed. Budgets bound what each request costs;
  capacity against a flood belongs to the network in front of the
  gateway, and to a rate limiter (Open questions).
- Storage capacity. A tenant can fill the cluster up to its quota, and
  radosgw's default quotas are unlimited
  (`rgw_user_default_quota_max_size`,
  [rgw.yaml.in:2326-2337](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2326-L2337),
  [rgw.yaml.in:2420-2431](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2420-L2431)).
  Capacity is the operator's quota policy, which rgw-go enforces as
  radosgw does, not a budget of the gateway's.
- The rest of the deployment: the Ceph daemons and their network, Rook,
  Kubernetes, the host, and any radosgw serving the same zone. A radosgw on
  the zone keeps its own defects, and what an attacker writes through it is
  state that rgw-go then serves.
- The operator's listener, until proposed decision D8 settles what of it
  the claim covers. Today `--metrics-addr` serves `/metrics` and
  `/debug/pprof/` with no authentication, and it is off by default.
- The S3 protocol's own weaknesses that clients rely on and radosgw serves
  too: SigV2's HMAC-SHA1 with no payload hash, unsigned payloads, presigned
  URLs as bearer tokens for their lifetime, and plain HTTP where the
  operator configures no TLS listener.
- Options radosgw has that switch a check off, such as
  `rgw_sigv4_insecure`, which keep radosgw's meaning because the operator
  sets them; the claim is made at their defaults.

### Assets

- **Object data and metadata**: the bytes, user metadata, tags, checksums
  and versions of every tenant's objects. Confidentiality and integrity
  against other tenants and anonymous clients; availability against loss
  through a defect, such as a GC or index race.
- **Authorization state**: ACLs, bucket and user policies, public access
  blocks, object lock retention and legal holds, MFA devices, admin caps,
  quotas, and the ownership of users, buckets and accounts. A change to
  them is a change to who may do what, so it is guarded as data access is.
- **Credentials**: users' access and secret keys and system users' keys,
  stored in RADOS by radosgw's formats; MFA seeds in the otp pool; the
  gateway's cephx key, which reaches the RGW pools; and, as later phases add
  them, STS session tokens and SSE-C keys in flight. None is ever logged or
  rendered beyond what radosgw's admin API returns to a caller with the
  caps for it.
- **The cluster**: its OSDs, whose class code a gateway feeds, and the
  shared RADOS state, the bucket index, GC queues, OLH logs and metadata,
  that radosgw and rgw-go must both keep consistent. A fault here is shared
  by every client of the cluster, not only the gateway's.
- **The gateway's availability**: the process, its memory, its goroutines
  and threads, its connections, and its share of the cluster's in-flight
  budget. One process serves every tenant, so its exhaustion is shared
  fate.
- **Accounting**: usage logs, user and bucket statistics, quotas and rate
  limit settings, on which tenants' bills and limits rest.

### Attackers and what is trusted

Three attackers, from the outside in:

- **Anonymous Internet clients** hold no key. They reach every byte of the
  frontend before authentication: the TLS handshake, the request line and
  headers, routing, the anonymous engine, the parse of a signed request
  that names a key they do not have, and the access-key lookup it drives.
  They also reach whatever a bucket's ACL or policy opens to everyone,
  static website serving, CORS preflights, and, once phase 2 routes POST
  object, a form whose fields rgw-go must read before the form's signature
  can be checked. A request with no signature is authenticated at once, as
  the anonymous user, so for them the boundary that matters is
  authorization: what a request no ACL or policy grants can still make the
  gateway do. They want data they were not granted, a crash or exhaustion
  that takes the gateway down, and anything the answers disclose.
- **Authenticated S3 users and tenants** hold keys of their own, so they
  also reach everything behind authentication: every op, every request
  body parser, and the documents they store, policies, lifecycle, CORS,
  website and tagging configurations, which rgw-go later parses and
  evaluates in other requests and in its workers. A malicious tenant on a
  shared gateway wants other tenants' data or accounting, more than its
  share of the shared process and cluster, or a crash that is shared fate.
  A user holding admin caps is still this attacker: the caps grant the
  admin operations they name and nothing else, and its input is checked as
  strictly as an S3 client's, since an admin cap holder can fault an OSD
  through radosgw ("cls_otp's otp_set puts an array as long as the seed on
  the OSD's stack" in the registry).
- **Holders of low-privilege Ceph keys on the same cluster**, such as an
  RBD or CephFS client, never speak to rgw-go. With caps on other pools
  they can load the cluster, which only Ceph's own scheduling bounds. With
  read caps on the zone's control pool they can send the notifies that
  keep metadata caches coherent; rgw-go treats any such notify as a reason
  to drop and re-read an entry, never as data to cache. A flood of notifies
  can so keep the metadata cache empty, and every request then reads from
  RADOS as a gateway without a cache would, but it cannot make rgw-go serve
  wrong data. Notifies are not rate-limited, because a real one skipped
  would leave stale permissions cached. With read caps on the RGW pools
  they read every stored key and object, and with write caps they can
  write anything there: the gateway cannot defend data whose pools its
  attacker can write.

Trusted, each for its reason:

- **The operator** and everything the operator sets: Ceph configuration,
  the mon config store, the beast spec, rgw-go's flags and environment, the
  keyring, the derived image, the host, and the system users whose keys
  may act as any user. They decide how the process runs and no request can
  change them. Values that would crash rgw-go or the cluster are still
  refused or floored at startup, never left to fault on first use, since
  an operator's typo is an outage, not an attack.
- **The cluster's daemons**, monitors and OSDs, which cephx authenticates
  and which hold the data whatever rgw-go does. Their replies are trusted
  for meaning but decoded as defensively as stored state (below).
- **Stored RADOS state**, trusted for authenticity but not for form
  (proposed, D3). Whoever can write the RGW pools already has the gateway's
  power, so its content cannot be defended at the gateway; but corruption,
  version skew, partial writes and radosgw's own defects do leave malformed
  records, so a record that does not decode fails the request that meets
  it, never the process.
- **radosgw on the same zone**, as a peer writer of the shared formats
  (section 8); its control notifies are trusted only as invalidation
  hints.
- **librados and the go-ceph fork**, in-process C and its bindings, which
  rgw-go cannot isolate: a fault there ends the process, so nothing reaches
  them in a form they are known to mishandle (Defence of the cluster).
- **Rook**, as the operator's agent, which runs the gateway and reaches the
  admin API with keys the operator granted. Rook is trusted for intent, not
  exempted from checks: its admin-ops user is an admin cap holder like any
  other, so a defect in Rook, or a stolen key of that user, gains only what
  the user's caps grant.

### Trust boundaries and the request path

The request path is every component an untrusted byte reaches, from a
client directly or from a document a tenant stored. It is:

| Component | Code | Untrusted input |
|---|---|---|
| HTTP frontend | `internal/frontend`, over Go's `net/http` and `crypto/tls` | every byte of every connection: the TLS handshake, the request line, headers, and body framing, before anything is known about the client; HTTP/1.x only |
| Routing and URL decoding | the router and the unserved-API handler in `internal/cli/serve.go`; `internal/s3`'s request parse (Host, path, query, malformed escapes, NUL refusals, bucket and key names); `internal/vhost`; `internal/rgwtext` | the request target and Host, decoded once, and the choice of S3, admin API or an unserved API's refusal |
| Dispatch and headers | `internal/s3` (dispatch, route sets, headers, request attrs, copy source); the header readers in `internal/op` (conditions, ranges, checksums, MFA); the value parsers they call in `internal/iso8601`, `internal/strptime`, `internal/cksum`, `internal/tags`, `internal/objectlock`, `internal/sse`, `internal/acl` and `internal/cors` | every request header, including `x-amz-meta-*`, grants, dates, ranges, ETag lists, `x-amz-copy-source` and CORS preflight headers |
| Authentication | `internal/auth`: the anonymous engine; SigV4 by header, query and presigned URL, with aws-chunked and trailer payloads verified as they stream; SigV2 by header and presigned URL; the POST object form's signature; the CORS preflight that resolves a key without a signature compare; a system user's `rgwx-uid`; and the access-key lookup they drive | the Authorization header, query credentials, the signed payload. No STS, Keystone or LDAP engine exists; STS joins this row when phase 3 adds it |
| Request bodies | `internal/op`'s parameter-body reader; `internal/xmltext` and every XML document decoder, in `internal/s3`, `internal/op`, `internal/acl`, `internal/cors`, `internal/lifecycle`, `internal/tags`, `internal/website`, `internal/objectlock` and `internal/sse`; IAM policy JSON in `internal/policy`; POST policy JSON in `internal/jsonspirit` and `internal/postpolicy`; the POST object form in `internal/s3`; aws-chunked framing in `internal/auth` | every body an op parses. Object data streams through `internal/op` and `internal/driver` unparsed, checksummed by `internal/cksum` and, from phase 2, compressed by `internal/compression` |
| Admin API | `internal/admin` (resource and argument parse, dispatch, caps, formats) and its ops in `internal/op`, with metadata documents decoded through `internal/meta` and `internal/jsonspirit` | the same as S3, at `rgw_admin_entry` on the same listener with the same authentication, plus JSON documents from cap holders |
| Authorization and stored configuration | `internal/authz`, `internal/policy`, `internal/acl`, `internal/objectlock`, `internal/cors`, `internal/website`, `internal/lifecycle`, and the workers that evaluate them | the request's condition environment, and every policy, ACL and configuration a tenant stored, through rgw-go or radosgw |
| Responses | `internal/formatter`, `internal/xmltext`'s escaping, and `internal/s3`'s and `internal/admin`'s error documents and response headers, response overrides, website redirects and CORS headers included | request and stored bytes reflected to clients: keys, metadata, redirect targets |
| Store and cluster boundary | `internal/driver` (names, manifests, the index protocol, the metadata cache and its control watch, the workers); `internal/meta` and `internal/denc`; `internal/cls/...`; `internal/compression`'s decoders; `internal/radosclient` and `internal/radosclient/goceph`, with the go-ceph fork beneath them | names, metadata and data from requests, encoded into RADOS operations and class requests; stored state and class replies, decoded |
| Notification delivery | none yet: the topic actions' endpoint parser and the HTTP and HTTPS deliverer join this row when phase 3 adds them | the push endpoint a topic's owner stores, a URL the gateway then dials, and every byte its destination sends back |
| Operator listener | `--metrics-addr`'s server in `internal/cli/serve.go`, with `internal/metrics` and `net/http/pprof` | any byte from whoever reaches its address; it is off by default, and what of it the claim covers is proposed decision D8 |

Some of this code no route reaches yet: the POST object form reader, its
signature check and `internal/postpolicy` wait for phase 2 to serve POST
object, as the ownership controls ops do. They are on the path already,
since a route is all it takes to expose them.

rgw-go has no health endpoint of its own: Rook's probes are an anonymous
`GET /` to the endpoint root, a ListBuckets that reads nothing from RADOS.
The rows above cover what a probe parses, not whether it is answered. A
probe counts against `rgw_max_concurrent_requests` as any request does,
as in radosgw, so while a client holds that cap the probe is answered 503
SlowDown at once. Rook's probe script takes a 503 as throttling and passes
([rgw-probe.sh:40-52](https://github.com/rook/rook/blob/v1.21.1/pkg/operator/ceph/object/rgw-probe.sh#L40-L52)
at Rook v1.21.1, the release the disposable clusters run; every release
since v1.11 has it), so the cap does not take Rook's pods out of service,
and rgw-go keeps that behaviour. A health check that passes only 200 to
399, as Kubernetes' own HTTP probe does
([rgw-probe.sh:43-45](https://github.com/rook/rook/blob/v1.21.1/pkg/operator/ceph/object/rgw-probe.sh#L43-L45)),
fails while the cap is held and takes the gateway out of whatever service
it guards; an operator who points one at rgw-go, as at radosgw, accepts
that. A connection past the connection cap is answered 503 SlowDown too
(Resource budgets), so a probe also passes under a flood of connections.

Not on the request path: `cmd/rgw-go`, `internal/cephconf`,
`internal/cli`'s command line (`root.go`, `signals.go`),
`internal/version`, the packages only tests import (`internal/testutil`,
`internal/memstore`, `internal/op/conformance`,
`internal/denc/goldentest` and the counterfeiter packages, whose names end
in `fakes`), every `_test.go` file, `hack/` and `test/` apart from the pins
below, and CI configuration. They take operator or developer input, and
PP-14's ruling stands for them: they are guarded against honest mistakes.
One of them is a security boundary all the same, the privilege drop in
`internal/cephconf`, and is reviewed as the request path is.

A pull request changes the request path when it changes a non-test file of
a package in the table, `go.mod` or `go.sum` (a dependency or the Go
toolchain under the frontend, the parsers or the cgo boundary), the go-ceph
fork's pin, or the Ceph image pin that chooses the librados a release
ships, each release's `cephImage` in its rooket chart values, which
`.goreleaser.yaml` mirrors. A new package joins the table in the change
that adds it, in the row its input puts it in.

Each boundary owes the rest of the system these properties:

- **Before a signature is checked**, only bounded work: the header within
  its budget, the route, the reads that resolve the access key to its user
  and account, and, for a POST object form, the fields before its file
  within their budget.
- **Before authorization**, no body is parsed, except a body whose content
  the authorization itself reads, such as a multi-object delete's keys,
  and that within its budget. A request no ACL or policy grants, the
  anonymous one included, costs the gateway its header, its route and the
  reads its authorization needs, and, for such a request, its body within
  its budget: a multi-object delete reads its body, unparsed, within
  `rgw_max_put_param_size`, 1 MiB by default, before its permission check.
- **Decoding** happens once, and a malformed escape or a NUL that would
  reach a RADOS name is refused before any handler or lookup.
- **Authentication** compares every signature in constant time, logs no
  credential, and lets no body byte take effect before the payload's
  verdict on it.
- **Parsers** never panic, never recurse past a fixed depth, and allocate
  at most a fixed multiple of their input, which the input's own budget
  bounds.
- **Responses** escape every reflected byte for its context, never put CR,
  LF or NUL into a header, and render no secret.
- **Outbound connections** that a tenant directs reach no loopback,
  link-local or cloud-metadata address the operator has not allowed, and
  no destination the operator refuses. There are two in phase 3. Its HTTP
  and HTTPS notification deliverer dials a URL the
  topic's owner chooses, and radosgw lets that URL name any destination:
  it checks only that the URL parses and that credentials in it arrived
  over TLS (`validate_and_update_endpoint_secret`,
  [rgw_rest_pubsub.cc:40-82](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_pubsub.cc#L40-L82),
  [rgw_rest_pubsub.cc:40-93](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_pubsub.cc#L40-L93)),
  and its deliverer posts to it, guarded only by `verify-ssl`
  ([rgw_pubsub_push.cc:75-100](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/driver/rados/rgw_pubsub_push.cc#L75-L100),
  [rgw_pubsub_push.cc:77-103](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/driver/rados/rgw_pubsub_push.cc#L77-L103)).
  Its AssumeRoleWithWebIdentity fetches an OpenID Connect provider's keys
  from the token's issuer once the issuer matches a provider that an
  account registered with CreateOpenIDConnectProvider (`load_provider`
  and `get_cert_url`,
  [rgw_rest_sts.cc:251-255](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_sts.cc#L251-L255),
  [rgw_rest_sts.cc:309-311](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_sts.cc#L309-L311),
  [rgw_rest_sts.cc:253-257](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_sts.cc#L253-L257),
  [rgw_rest_sts.cc:311-313](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_sts.cc#L311-L313)),
  so an account chooses that URL too. A tenant can so make a gateway
  inside the cluster's network connect to the gateway's own loopback, to a
  link-local address or to a cloud provider's metadata service. rgw-go
  refuses loopback, link-local and cloud-metadata destinations by default,
  judged on the address each connection it opens actually reaches after
  name resolution, so that a name that resolves to one address when
  checked and another when dialled gets no further. An operator option
  adjusts the set both ways: it refuses destinations rgw-go cannot know
  to, such as the cluster's own networks, and it allows a refused one an
  operator delivers to on purpose, such as a receiver on the gateway's own
  host. An allowance names its destinations one by one, so it never
  switches the refusal off as a whole (D2). The refusal is a security
  difference, recorded in `docs/differences.md` when phase 3 implements
  the deliverer and STS, and the topic actions' endpoint parser is a
  parser under the rules here and in Assurance, fuzz target included.
- **A panic** in a request's own goroutine is answered 500 and the process
  goes on. A panic in any other goroutine, exhaustion of memory or of a
  goroutine's stack, or a fault in C ends the process, so no input may
  reach one: a goroutine a request starts touches request data only
  through code on this path, and depth and memory are budgeted.

### Security differences from radosgw

Section 8 already has rgw-go do the correct thing where radosgw is wrong.
For security the rule is stricter, and it has one form, since rgw-go has
one behaviour:

- **Fail closed.** Where radosgw's behaviour is a vulnerability, or unsafe
  for a gateway the Internet reaches, rgw-go refuses the request, or the
  part of it that is unsafe, with the error S3 defines for the condition,
  or radosgw's error for the nearest condition it refuses. It never accepts
  the input with another meaning, never drops part of it silently, and
  never answers success for a check it skipped. Where upstream has fixed
  the defect in a later release, the fix is the model, on both floors.
- **Refuse on write, serve on read, make output safe** (proposed, D4).
  Hardening applies to new input. State that radosgw or an earlier release
  stored is read as radosgw reads it, so that no tenant loses data to a
  check it never met, unless serving it as stored is itself the
  vulnerability; then rgw-go neutralizes it on the way out, as a stored
  header value's CR and LF go out as spaces. A decoder of stored state
  admits whatever radosgw's write path can store, so a bound set on
  requests never fails the read of state radosgw wrote; only where serving
  a stored thing would itself exhaust the gateway does the bound win, and
  then the read fails closed and the difference's entry says so. What
  rgw-go writes stays in radosgw's formats, so a radosgw on the zone reads
  it.
- **Recorded in the same change.** The difference gets its
  `docs/differences.md` entry in the change that introduces it, under the
  heading of the area it belongs to, naming radosgw's behaviour at v19.2.7
  and v20.2.4 and what a client sees instead. `docs/exclusions.md` lists
  only features rgw-go does not implement, so a hardening never goes
  there. A difference that stems from an upstream defect also has its
  registry entry with its security class, whose rgw-go line names the
  difference's entry and the test that pins it (Assurance), and, where
  reasonable, an s3-tests test on jhoblitt/ceph's `rgw-go-s3tests` branch
  that exercises it.
- **The parity ledgers keep radosgw's outcomes.** Such a test passes on one
  gateway and fails on the other, so the parity check of section 9 sees a
  difference, and the release's known-differences file lists it in the
  same change, under a comment naming the difference's entry. A release
  whose radosgw already behaves safely needs no line. The baseline is never
  edited to make a difference disappear: it records what radosgw does.
  When upstream fixes the defect and the baseline is recorded again at a
  release with the fix, the outcomes agree, the known-differences line
  fails the check as stale, and its removal, with the entry's update,
  records that radosgw caught up.
- **A client that relied on radosgw's laxness gets the refusal**: a status
  and an error code it can act on, where radosgw accepted, documented in
  `docs/differences.md`. It never gets its request served with a changed
  meaning. No setting restores radosgw's behaviour (proposed, D2), since a
  setting that switched a hardening off would be the rejected compatibility
  profile under another name. A limit radosgw reads from a Ceph option stays
  configurable through that option, with radosgw's meaning (Resource
  budgets).
- **The acceptance criterion still binds.** A hardening that would fail
  section 1's criterion is raised with the owner before it lands.

### Defence of the cluster

A gateway is the main way untrusted bytes reach OSD class code, and a
faulted OSD is every client's loss, not only the gateway's. The registry
records class methods that fail on input radosgw passes them unchecked:
cls_otp divides by a stored step of 0, puts an array as long as a
device's seed on the OSD's stack, and walks a window as wide as the stored
one on every check. So:

- **rgw-go never sends a class method a request-shaped input the registry
  records it mishandling** (proposed, D1). A request-shaped trigger is one
  the class request itself carries, such as otp_set's seed, step and
  window. Its guard belongs in the class client, the last code before the
  exec step, so that it holds for every caller, present and future; the op
  that takes the input from a client refuses it earlier, with the S3 or
  admin error the client should see. Today the otp metadata put refuses a
  step of 0 or over 3600 seconds, a window over 1024 steps and a seed over
  1024 bytes in the op, and the class client gains those guards as planned
  work.
- **A state-shaped trigger**, one the class reads from what is already
  stored, never passes the class client, so it is guarded where rgw-go
  reads that state (proposed, D1): rgw-go reads the state first, sends no
  class call for state the registry records the class mishandling, and
  makes the call it does send conditional on the state being as it read
  it, in the same operation, so that no other writer changes it in
  between. The MFA check works so: it reads the device, calls the class
  for no device of step 0, of a step over 3600 seconds or of a window over
  1024 steps, and compares the device's record in the operation that sends
  the check. What rgw-go cannot do is repair such state, which radosgw or
  an older release stored and which still reads back, or keep a radosgw on
  the zone from calling the class with it; and a device rgw-go will not
  check is one its owner cannot use through rgw-go. The registry entry
  names that exposure.
- **Where the class decides from state rgw-go cannot see or change**, such
  as its own records inside the bucket index, a defect can fire on a
  legitimate request whoever sends it. rgw-go then mitigates what it can,
  as its MFA check refuses a code the class's replay accounting would
  accept again, and the registry entry names the exposure that remains.
- **What rgw-go sends is bounded.** Every RADOS operation built from a
  request has sizes the request's budgets bound, within what the OSDs
  accept, and the operations one request causes are bounded per op at
  radosgw's own counts, so that what a request costs the cluster is known
  and pinned. The in-flight limiter of section 7 caps what the gateway has
  outstanding.
- **Stored state and class replies are decoded defensively** (proposed,
  D3). A decoder refuses a record that does not decode and never allocates
  by a stored length past a bound. A request that meets such a record
  fails with an internal error and a log line naming the object, a worker
  skips it, and the process goes on.
- **Control notifies are hints.** A notify on the zone's control objects
  drops the cache entry it names, and the next read fetches the entry
  again; its payload is never cached. None is rate-limited, so a flood of
  them costs reads but never correctness (Attackers).

The cgo boundary rests on these assumptions. The seam's specs check the
NUL refusal, the in-flight limiter and the completion's hold on an
abandoned operation's buffers, under the race detector and cgo checks
(section 14); the first assumption and the last are ones no test can
check:

- A fault in librados or in the bindings ends the process with every
  request it serves, and nothing recovers from one, so input reaches C
  only in forms it is known to handle.
- C reads every name up to its first NUL, so no name holding a NUL reaches
  librados: the request checks refuse one, and beneath them the seam
  refuses any object name, locator, xattr name, or omap listing start or
  prefix that holds one (`docs/cgo-limitations.md`).
- A buffer pinned for an operation belongs to its completion until
  librados reports the operation done, an abandoned one's included, since
  a write already sent may still apply and C may still read the buffer.
  Cancellation never frees memory C may read.
- librados blocks the submitting OS thread once its objecter throttle is
  spent, so every submission waits on rgw-go's in-flight limiter first, and
  a flood of requests costs goroutines, not threads.
- A read's buffer is sized before the call, from a stat or a fixed bound,
  so a size taken from stored state must be capped before it sizes one.
- librados aborts the process on some cluster states, such as a listing
  that seeks after its pool was deleted, which rgw-go meets as radosgw
  does (registry). No request can delete a pool, so it stays an
  operational hazard, not an attack.

### Resource budgets

Every resource a client can make the gateway spend has a budget, and each
budget is one of three kinds, which differ in what a request past one
meets:

- **Admission budgets** refuse a request before it starts, with the error
  S3 or radosgw gives for the condition: a header, a body or a document
  past its bound, a request past `rgw_max_concurrent_requests`. The memory
  budget's first draw is one: a transfer whose first buffer finds the
  budget spent waits at most 5 s and is then answered 503 SlowDown, before
  any byte of its response and, for a PUT, before its body is read. A
  refusal at the start costs the client a retry and the gateway nothing.
- **Hold budgets** bound what a started request holds. A draw past one
  waits for room and never fails a transfer mid-flight, since failing it
  would throw away what the transfer has already moved and leave the
  client to send it all again. A wait lasts as long as the requests that
  hold the room take, slow clients among them, so each hold budget's row
  names the slow-client exposure it leaves open.
- **Connection caps** refuse a connection past the cap by accepting it
  only to read its request header, under the header budget and the client
  I/O timer, answer 503 SlowDown and close it. A client gets an error it
  can retry rather than a connect timeout, and Rook's probe, which takes a
  503 as throttling, still passes. The refusal holds a descriptor and a
  goroutine for that moment only.

Time is bounded by two clocks rather than drawn on, besides the planned
minimum transfer rate of the progress row: the client I/O timer,
`request_timeout_ms` for each read of a body and each write of a response,
armed as beast arms it, which rgw-go copies; and a deadline on each wait
for the cluster (Waits on the cluster, below).

A budget's value comes from, in order:

1. radosgw's own limit where it has one, read from the same Ceph option
   with the same default, per release where the releases differ, so that a
   client meets the same refusals and an operator's tuning carries over;
2. AWS S3's documented limit for the same quantity where radosgw has none,
   since S3 clients are written against it;
3. measurement where neither exists: the plan measures what the largest
   legitimate requests in s3-tests, Rook's suite and the benchmark profiles
   need, adds a margin, and checks the budgets together against the memory
   and file descriptors the gateway has, so that every admitted request at
   its budget at once still fits.

A budget is stricter than radosgw's where radosgw's limit, or its lack of
one, is itself the defect: where it lets a request or a client hold
resources out of proportion to what it sends, or reach code the registry
records failing. The stricter budget is then a security difference under
the rule above. A budget radosgw has no option for and whose right value
depends on the host, as a connection cap does, is an rgw-go flag, which can
bound and never disable (proposed, D2), as `--memory-budget-ratio`
already is (section 5).

| Budget | radosgw at the floors | rgw-go |
|---|---|---|
| Request header | 16 KiB of `header_limit`, raised by the beast key `max_header_size` up to its 64 KiB parse buffer ([rgw_asio_frontend.cc:403](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L403) and [rgw_asio_frontend.cc:594-607](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L594-L607), [rgw_asio_frontend.cc:414](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L414) and [rgw_asio_frontend.cc:595-608](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L595-L608)); no count of fields | radosgw's limit, from the beast spec, which net/http enforces with 4096 bytes of slack; a `max_header_size` of 0, which makes radosgw refuse every request, means net/http's 1 MiB (`docs/differences.md`, "Header limit and malformed requests") |
| Header, idle and each body read or write | `request_timeout_ms`, 65 s by default (`REQUEST_TIMEOUT`, [rgw_asio_frontend.h:11](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.h#L11), [rgw_asio_frontend.h:11](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.h#L11)), armed for the whole header and for each read or write of a body (`recv_body`, [rgw_asio_frontend.cc:151-174](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L151-L174), [rgw_asio_frontend.cc:152-175](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L152-L175)) | the same per operation; on a kept-alive connection the idle wait and the next header are timed separately, each by `request_timeout_ms` (`docs/differences.md`, "Timeouts per operation") |
| Waits on the cluster | none by default: the one deadline librados offers, `rados_osd_op_timeout`, is process-wide and 0, off ([global.yaml.in:6402-6409](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/global.yaml.in#L6402-L6409), [global.yaml.in:6538-6545](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/global.yaml.in#L6538-L6545)); set, it completes every OSD operation that outlasts it with ETIMEDOUT, a write's commit steps included (`op_cancel`, [Objecter.cc:2317-2324](https://github.com/ceph/ceph/blob/v19.2.7/src/osdc/Objecter.cc#L2317-L2324), [Objecter.cc:2385-2392](https://github.com/ceph/ceph/blob/v20.2.4/src/osdc/Objecter.cc#L2385-L2392)) | today none: a request waits on a stalled cluster until its client leaves; the rule: each wait 60 s, then 503 SlowDown, a write's commit steps excepted (Waits on the cluster, below) |
| A request's progress | none: a request body trickled within the per-read timeout holds its request, and its share of the concurrency cap, without end, and beast sets no `body_limit` ([rgw_asio_frontend.cc:248-258](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L248-L258), [rgw_asio_frontend.cc:249-259](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L249-L259)); a response drained just fast enough for each write to finish within the timeout holds its read window, up to 16 MiB, for the whole object, so 1024 such readers hold up to 16 GiB | stricter: a minimum transfer rate for every request body and every response, and a deadline for a body read whole; measured |
| Open connections | none; `max_connection_backlog` sizes only the listen backlog ([rgw_asio_frontend.cc:694-704](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L694-L704), [rgw_asio_frontend.cc:702-712](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L702-L712)) | stricter: a cap on open connections, past which a connection is answered 503 SlowDown and closed, and a cap on idle ones; measured |
| Open files | a descriptor per open connection, which nothing bounds but the process's limit; at that limit `accept` backs off and retries (`backoff_sleep`, [rgw_asio_frontend.cc:1084-1111](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L1084-L1111), [rgw_asio_frontend.cc:1051-1078](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L1051-L1078)) | a descriptor per open connection, which only the connection cap above will bound, and librados's sockets to the monitors and OSDs; no request opens a file of its own |
| Goroutines | a coroutine per open connection (`on_accept`, [rgw_asio_frontend.cc:1113-1164](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_asio_frontend.cc#L1113-L1164), [rgw_asio_frontend.cc:1080-1126](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_asio_frontend.cc#L1080-L1126)) | one per open connection, which only the connection cap will bound; for each request the concurrency cap admits, those its op starts, a fixed few or as many as radosgw's own option for the op allows, such as `rgw_bucket_index_max_aio` and `rgw_multi_obj_del_max_aio`; and, outliving its request, one for each index completion or abandoned operation it leaves waiting on the cluster, which the in-flight limiter bounds once it admits them and nothing yet bounds while they wait for it |
| Concurrent requests | `rgw_max_concurrent_requests`, 1024, past which a request is answered 503 SlowDown ([rgw.yaml.in:3471-3483](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3471-L3483), [rgw.yaml.in:3650-3662](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3650-L3662)) | the same option, S3 and admin requests in one count |
| Refused requests | uncounted: the rate limit runs after `verify_permission` ([rgw_process.cc:221-250](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L221-L250), [rgw_process.cc:221-253](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L221-L253)), so a request refused for its signature or its permissions is never charged; an access key's index is read through the system-object cache (`read_index`, [svc_user_rados.cc:650-668](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_user_rados.cc#L650-L668), [svc_user_rados.cc:626-644](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_user_rados.cc#L626-L644)), which keeps a missing object as a negative entry (`ENOENT`, [svc_sys_obj_cache.cc:200-205](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/services/svc_sys_obj_cache.cc#L200-L205), [svc_sys_obj_cache.cc:200-205](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/services/svc_sys_obj_cache.cc#L200-L205)) | the same today: each unknown access key costs a RADOS read and a negative entry in the metadata cache of section 7, so a flood of random keys costs reads without bound and evicts real users' entries; open question 2 |
| Parameter bodies | `rgw_max_put_param_size`, 1 MiB ([rgw.yaml.in:138-145](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L138-L145), [rgw.yaml.in:144-151](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L144-L151)); an admin metadata document of any length | the same option; an admin metadata document bounded at 16 MiB |
| Document structure | none beyond the body's size: every XML element is built, and json_spirit recurses with no limit (registry) | stricter: a fixed nesting depth, and a parse's allocation bounded by a multiple of its body; measured |
| Objects and uploads | `rgw_max_put_size`, 5 GiB ([rgw.yaml.in:127-137](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L127-L137), [rgw.yaml.in:130-143](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L130-L143)); `rgw_multipart_part_upload_limit`, 10000 parts ([rgw.yaml.in:2373-2380](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2373-L2380), [rgw.yaml.in:2467-2474](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2467-L2474)); `rgw_multipart_min_part_size`, 5 MiB ([rgw.yaml.in:2363-2372](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L2363-L2372), [rgw.yaml.in:2457-2466](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L2457-L2466)) | the same options |
| Item counts | `rgw_acl_grants_max_num` 100, `rgw_user_policies_max_num` 100, `rgw_cors_rules_max_num` 100, `rgw_delete_multi_obj_max_num` 1000 and `rgw_website_routing_rules_max_num` 50 ([rgw.yaml.in:7-53](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L7-L53), [rgw.yaml.in:7-53](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L7-L53)); `rgw_lc_max_rules` 1000 ([rgw.yaml.in:438-446](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L438-L446), [rgw.yaml.in:464-472](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L464-L472)); `rgw_max_listing_results` 1000 on Squid and 5000 on Tentacle ([rgw.yaml.in:3434-3446](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L3434-L3446), [rgw.yaml.in:3613-3625](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L3613-L3625)) | the same options, per release |
| Names and metadata | object names of 1024 bytes (`MAX_OBJ_NAME_LEN`, [rgw_rest.h:556](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest.h#L556), [rgw_rest.h:568](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest.h#L568)); `rgw_max_attr_size`, `rgw_max_attr_name_len` and `rgw_max_attrs_num_in_req` 0, unlimited within the header ([rgw.yaml.in:146-176](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L146-L176), [rgw.yaml.in:152-182](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L152-L182)) | the same; the header budget bounds metadata |
| A POST form's fields before its file | none (registry) | stricter: `rgw_max_chunk_size` plus 1 MiB of the body, refused InvalidArgument with the description AWS S3 gives its MaxPostPreDataLengthExceeded |
| Memory for object data | each PUT's writes in flight to `rgw_put_obj_min_window_size`, 16 MiB ([rgw.yaml.in:99-113](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L99-L113), [rgw.yaml.in:102-116](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L102-L116)), and each GET's reads to `rgw_get_obj_window_size`, 16 MiB ([rgw.yaml.in:1905-1913](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/rgw.yaml.in#L1905-L1913), [rgw.yaml.in:1999-2007](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/rgw.yaml.in#L1999-L2007)); no bound across requests | one budget across requests for the buffers of object data every GET, PUT and copy holds (section 7): `--memory-budget-ratio` of the Go memory limit, half by default, the limit itself `--memory-limit-ratio` of the pod's memory limit, 0.8 by default, so 819 MiB in a 2 GiB pod (`docs/memory.md`). A transfer's first buffer is an admission draw, which waits at most 5 s and is then answered 503 SlowDown; every later one is a hold draw, which waits as long as the client stays. Slow-client exposure: a client that sends a body or takes a response slowly holds its shares as long as it does, and about forty slow readers in a 2 GiB pod make every transfer wait (`docs/differences.md`, "Object data buffers share one memory budget, sized from the Go memory limit"). Still to come: the progress row's minimum transfer rate, and a ceiling on the shares one request holds; measured |
| Cluster work per request | the windows above and each op's own counts | the in-flight limiter under the objecter throttle (section 7), a hold budget: a submission past it waits for room, within the deadline on waits for the cluster, and room is held only while the cluster works on an operation, so a slow client holds none of it; and a ceiling per op on the RADOS operations one request causes, at radosgw's counts |
| Request rate | per user, per bucket, and one budget shared by every anonymous request, set through the admin API and charged only to requests authorization passes ("Refused requests" above); none per client address (`rate_limit`, [rgw_process.cc:94-163](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_process.cc#L94-L163), [rgw_process.cc:95-163](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_process.cc#L95-L163)) | settings stored since phase 1, enforced from phase 3 (Open questions) |
| Work before authorization | a POST form's fields read whole before its signature is checked, its policy and tagging parsed only after it (`get_policy` and `get_tags`, [rgw_rest_s3.cc:3063-3067](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3063-L3067), [rgw_rest_s3.cc:3202-3206](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3202-L3206); `Strategy::apply` before `from_json`, [rgw_rest_s3.cc:3190-3210](https://github.com/ceph/ceph/blob/v19.2.7/src/rgw/rgw_rest_s3.cc#L3190-L3210), [rgw_rest_s3.cc:3326-3346](https://github.com/ceph/ceph/blob/v20.2.4/src/rgw/rgw_rest_s3.cc#L3326-L3346)); and some ops' XML parsed before the request is authorized (registry) | the bounded work the request path allows (above) |
| The operator listener | radosgw has no such listener: its counters reach Prometheus through its admin socket (section 9) | today a 10-second header timeout and nothing else: no write or idle timeout and no connection cap of its own; proposed (D8): the frontend's header and idle timeouts, a write timeout, and a connection cap of its own |

Rows marked measured get their values from the plan, by rule 3. Every
budget is pinned by a test of its kind. An admission budget's test serves
a request at the limit and refuses one past it with the budget's error,
before the resource is spent. A hold budget's test pins the wait: a draw
past the budget waits, the transfer completes once room frees, and
nothing it had moved is lost. A connection cap's test shows a connection
past the cap answered 503 SlowDown and closed while one under it is
served, and a clock's test
shows that what it times ends at its deadline with the answer this
section gives. The denial-of-service suite drives many at once. It opens
connections and holds them idle, trickles headers and bodies, drains
responses at the slowest rate the per-write timeout allows, sends
documents at the size and depth bounds and multipart forms at theirs,
signs requests under random access keys, starts more large transfers
than the memory budget admits, and holds back the cluster's replies, each
while a well-behaved client keeps working; it asserts that the gateway's
memory, goroutines and open files stay within their budgets and that the
well-behaved client is served. It runs in-process in the unit suite where
no cluster is needed, and against the disposable clusters for the budgets
that involve RADOS.

**Waits on the cluster.** A request waiting on a stalled cluster holds its
concurrency slot, its memory shares and its goroutines for as long as the
cluster takes, and no clock of the client's ends the wait, since the
client is waiting too. Enough such requests fill
`rgw_max_concurrent_requests`, and the gateway, though up, answers every
other request 503 SlowDown. Today only the client's departure ends such a
wait, apart from a few cleanup steps that bound their own. The rule: each
wait on the cluster, for an operation's reply or for room under the
in-flight limiter, is limited to 60 s, and a request whose wait reaches
the limit is answered 503 SlowDown, or, once its response has begun,
ends where its body stopped, as any failure after the header does. The
limit is per wait, not per request, so a transfer of any length still
completes while the cluster keeps answering. 60 s is twice the 30 s after
which an OSD reports an operation as slow (`osd_op_complaint_time`,
[global.yaml.in:3152-3158](https://github.com/ceph/ceph/blob/v19.2.7/src/common/options/global.yaml.in#L3152-L3158),
[global.yaml.in:3206-3212](https://github.com/ceph/ceph/blob/v20.2.4/src/common/options/global.yaml.in#L3206-L3212)).
The deadline cancels a wait as a client's departure does, and reaches
only what that reaches. A write's commit steps stay shielded from
cancellation, as they are today, so that a commit that has started
finishes rather than leave its object half linked. A wait abandoned at
the deadline leaves its operation to the reaper that already takes
abandoned operations, which keeps the operation's buffers until librados
answers (section 7, "Cancellation"). A call made synchronously, such as a
lock taken through the bindings' synchronous call or any call in the
`sync` completion mode, cannot be abandoned and stays outside the rule
until it is sent asynchronously (`docs/cgo-limitations.md`). The deadline
is a difference from radosgw, recorded in `docs/differences.md` when it
is implemented. rgw-go does not rely on `rados_osd_op_timeout` instead,
because that option times out commit steps too, and a write it times out
may still have landed, leaving the gateway to act on a result it does not
know (registry, "radosgw's set_olh cancels the pending attr of a link that
timed out, though it may have landed"). Whether the 60 s is a flag, and
its name, follows the rule for budget flags above.

What the memory budget settles, and what it leaves open. The gateway
comparison of 2026-10-15 measured rgw-go's peak resident memory on 64 MiB
objects at 32 concurrent streams at 3.3 to 5.1 times radosgw's, 1.9 to
3.0 GiB against about 0.6 GiB, on both releases
(`docs/benchmarks/2026-10-15-gateway-squid/REPORT.md`,
`docs/benchmarks/2026-10-15-gateway-tentacle/REPORT.md`). The memory
measurements of 2026-11-02 to 2026-11-04 found the cause, mostly the Go
heap, whose pools of 4 MiB buffers grew with the traffic and which the
collector's default let reach twice its live size with no memory limit
set (`docs/benchmarks/2026-11-02-memory-tentacle/REPORT.md`). The Go
memory limit sized from the pod's, the buffer budget, and one free list
of buffers that the budget caps now bound it (`docs/memory.md`): in a
2 GiB pod, 32 concurrent 64 MiB GETs peaked at about 1.5 GiB, about twice
Tentacle radosgw's and within the pod
(`docs/benchmarks/2026-11-04-realworld-memory-tentacle/REPORT.md`).
Outside the budget and the Go limit are the C heap, which holds
librados's copies of data in flight under the in-flight limiter, and the
socket buffers the pod is charged for each connection, which the
connection cap will bound. What stays open is the slow client. The budget
never fails a transfer it has let start, so a client that sends or reads
slowly holds its shares as long as it keeps each read or write within
`request_timeout_ms`, and enough such clients make every new transfer
wait and then answer SlowDown, though their buffers stay within the
budget, where radosgw's would run the pod out of memory. The progress
row's minimum transfer rate, with a ceiling on the shares one request
holds, is what closes it.

### Assurance

Three practices back the claim, adopted with it.

**Adversarial review of the request path.** Every pull request that
changes the request path, as the table above defines it, gets an
adversarial review besides its task review: a reviewer takes the
attackers above, tries to use the change against the claim, and judges it
against this model. This widens PP-14's rule (`docs/porting-process.md`),
which kept adversarial review to the boundaries the spec named. Changes off
the path, the parity ledgers and their tooling among them, are still
reviewed against honest mistakes only.

**Fuzzing of every parser.** Every function that parses bytes a client
controls has a Go fuzz target, and so, if the owner confirms it (proposed,
D7), does every decoder of stored state:

- the request target, Host and query, and the admin API's arguments;
- every header value parser: dates, ranges, conditions and ETag lists,
  copy sources, grants, tags, checksums, MFA, encryption and object lock
  headers, and CORS preflight headers;
- authentication: SigV4 and SigV2 header and query parsing, the canonical
  request, aws-chunked framing and trailers, and a POST form's signature
  fields;
- every request body: the XML reader and each document decoder, IAM policy
  JSON, POST policy JSON, the multipart/form-data reader, and the admin
  API's documents;
- from phase 3, the topic actions' push endpoints and their arguments;
- stored state: the `denc` and `meta` decoders, class replies, manifests
  and the compression decoders.

Each target asserts that its parser never panics and allocates within its
multiple of the input; `make test`'s runs of the seeds check only that. In
the scheduled fuzz workflow alone, each input must also finish within
1 s, a margin thousands of times a parser's normal cost, so that the
bound catches a catastrophic slowdown without failing on a slow or loaded
runner. Where an encoder pairs with the decoder, the target asserts
idempotence, not byte identity: what decodes re-encodes to bytes that
decode to the same value and re-encode to themselves. Section 8 decodes
versions older than the one it writes and skips trailing fields it does
not know, so a decoded input need not re-encode to its own bytes.

`go test` runs every target's seed corpus, so the seeds run in `make test`
on every pull request. A new target lands with seeds that include its
parser's spec inputs, and an input a longer run finds is committed as a
seed in the pull request that fixes it. A scheduled workflow, beside
section 12's three, runs each target for a time the plan sets; its
failures are watched and triaged as a failed required check is. The
targets that exist are a start, and the plan lists the parsers that lack
one.

**Every security-rated registry entry becomes a regression test.** An
entry of `docs/ceph-upstream-bugs.md` that is security-rated, in the scope
proposed decision D6 sets, gets a test that shows rgw-go is not exposed,
and the entry's rgw-go line names it:

- a unit spec where rgw-go parses or decides the input;
- an s3-tests test on `rgw-go-s3tests`, under the project's rule, where a
  client can observe the behaviour;
- for a defect in OSD class code, cls_rgw, cls_otp, cls_user, cls_lock,
  cls_version or the 2pc queue: for a request-shaped trigger, a spec that
  the class client refuses the triggering input before the exec step; for
  a state-shaped one, a spec that the path that reads the state sends no
  class call for state the class mishandles, and sends the call it does
  send conditional on the state as read. fakerados's emulation of the class
  must fail the triggering input as the OSD would, so that a spec of any
  path that still sends it fails; where the emulation does not yet, the
  backfill task makes it.

Where rgw-go inherits a defect it cannot avoid (Defence of the cluster),
the test pins rgw-go's mitigation and the entry names the exposure that
remains. A new entry gets its test in the change that records rgw-go's
handling of it. The existing entries are backfilled by a numbered plan
task, which lists those still without a test.

### Decisions for the owner

Drafting decisions the owner has not yet made. The sections above apply
each as proposed until the owner approves or rejects it.

- **D1. Guards against class-code defects.** Proposed: a trigger the class
  request carries is refused in the class client as well as in the op that
  takes it from a client; a trigger in stored state is guarded where
  rgw-go reads that state, with the class call conditional on the state
  as read; and the registry names what neither guard reaches. Reason: the
  class client is the one place every caller passes, present and future,
  so a new path cannot forget the guard, while stored state reaches the
  class without passing the client, so only its reader can see it. It
  creates planned work: the otp guards sit in the op and the driver today.
- **D2. No setting switches a hardening off.** Proposed: rgw-go adds no
  option that restores an unsafe radosgw behaviour; radosgw's own options
  keep radosgw's meaning; and a budget with no Ceph option whose value
  depends on the host becomes an rgw-go flag that can bound and never
  disable, which widens section 5's list of rgw-go's own flags. Reason: a
  switch per check is the rejected compatibility profile under another
  name, while radosgw's options are the drop-in configuration operators
  already have. Cost: a client that depends on a lax radosgw behaviour has
  no setting to fall back on, so a drop-in deployment fixes the client; it
  gets the refusal "Security differences from radosgw" describes, where
  radosgw accepted.
- **D3. Stored state is trusted for authenticity, not for form.** Proposed:
  a record that does not decode, or would exhaust the gateway, fails the
  request or the worker item that meets it, never the process. Reason:
  whoever can write the RGW pools holds the gateway's power, so
  authenticity cannot be defended at the gateway, but malformed records
  occur without an attacker, from corruption, version skew, partial writes
  and radosgw's own defects, and a crash is every tenant's outage.
- **D4. Refuse on write, serve on read, make output safe.** Proposed: as
  "Security differences from radosgw" states it, decoders admitting
  whatever radosgw's write path can store, and the bound winning only
  where serving the stored thing would exhaust the gateway. Reason: a
  tenant must not lose its data over a check it never met, which keeps a
  rolling replacement safe, but a stored payload that attacks its readers
  must not reach them. It also decides that a tenant's existing unsafe
  state keeps being served, neutralized where that is the vulnerability.
- **D5. The claim is a target with exit conditions.** Proposed: the claim
  is met once every budget in the table is enforced and pinned by its
  test, the denial-of-service suite passes, the memory budget's
  slow-client exposure is closed by the minimum transfer rate and the
  ceiling on one request's shares, the registry backfill is done and the
  open questions are settled. The owner declares it met, and the README
  and the notes of the release that meets it say so; until then neither
  claims fitness for the Internet. Reason: operators act on the claim
  where they read it, so it should appear there only once it holds, and
  from the person accountable for it.
- **D6. Which registry entries are security-rated.** Proposed: an entry
  counts when its Kind line or a triage rates it security-relevant,
  possibly or conditionally so included: about 106 of the registry's 470
  entries on 2026-10-31, roughly half rated outright and half possibly or
  conditionally. Counting outright ratings only would halve the backfill.
  Reason: "possibly" and "conditional" mark entries whose exposure nobody
  has measured, and a regression test is the cheapest way to settle it
  for rgw-go.
- **D7. Fuzzing stored-state decoders.** Proposed: besides every parser of
  client bytes, every decoder of stored state and of class replies gets a
  fuzz target. Reason: D3 promises that a malformed record fails a request
  rather than the process, and fuzzing is how that promise is checked; it
  multiplies the number of targets.
- **D8. The operator listener.** Today `--metrics-addr`, off by default,
  serves `/metrics` and every pprof handler, the command line, CPU
  profiles, traces and the runtime's named profiles, with no
  authentication, a 10-second header timeout and nothing else, on the file
  descriptors the frontend also needs. The options: keep it as it is,
  outside the claim; put pprof behind a flag of its own, off by default;
  or bind the listener to loopback by default. Proposed: pprof behind its
  own off-by-default flag and outside the claim, and `/metrics` covered by
  the claim when enabled, under the listener budgets in the table. Reason:
  Prometheus scrapes `/metrics` from another pod, which a loopback default
  would defeat, while pprof discloses the command line and spends CPU on
  request, which no scrape needs.

### Open questions for the owner

Listed, not decided:

1. **Disclosure of rgw-go's own vulnerabilities.** Section 8 and PP-4 make
   every defect the project finds public as soon as it is found, which
   suits the upstream defects the registry holds. Once rgw-go is deployed
   where the Internet reaches it, a public record of a defect in rgw-go
   itself tells attackers before a fixed release exists. Whether rgw-go's
   own vulnerabilities keep that rule, or go through a private channel,
   such as GitHub's private vulnerability reporting with an advisory
   published beside the fix, is open.
2. **A rate limiter as a prerequisite.** rgw-go enforces no rate limit
   today. Phase 3 enforces radosgw's, which counts per user, per bucket,
   and one budget for every anonymous request together, nothing per client
   address, and charges only requests that pass authorization: a request
   refused for its signature or its permissions is never counted. A flood
   of requests signed under random access keys therefore costs each
   gateway a RADOS read and a negative cache entry per key, evicting real
   users' entries, and no limiter radosgw has sees it (Resource budgets).
   Open: whether the claim waits for a limiter, and whether it needs one
   radosgw lacks, per client address or charging refused requests, which
   would be a security difference under the rule above and would key on an
   address that a proxy in front of the gateway may hide.
3. **Where hardening ranks among section 2's objectives**, whose first is
   performance. A budget can cost throughput, as a lower memory ceiling per
   large transfer would; which wins when the two conflict is open.
